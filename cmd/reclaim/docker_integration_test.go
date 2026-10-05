package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/stretchr/testify/require"

	"github.com/mandloideep/reclaim/internal/dockerx"
	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/plan"
)

// The Docker integration test runs against a real daemon only when
// RECLAIM_DOCKER_TESTS=1. It creates its own fixtures, all labeled
// reclaim.test=1, and checks that the scanner finds exactly those fixtures
// when filtered by the label, that apply removes the selected ones, and that
// nothing without the label changes. It never removes the busybox base image.

const (
	testLabelKey   = "reclaim.test"
	testLabelValue = "1"
)

func dockerClient(t *testing.T) *client.Client {
	t.Helper()
	if os.Getenv("RECLAIM_DOCKER_TESTS") != "1" {
		t.Skip("set RECLAIM_DOCKER_TESTS=1 to run Docker integration tests against a real daemon")
	}
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	cli, err := dockerx.New(os.Getenv, home)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cli.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = cli.Ping(ctx)
	require.NoError(t, err, "RECLAIM_DOCKER_TESTS=1 but the daemon is not reachable")
	return cli
}

// unlabeledState lists every object without the test label, so the test can
// prove it did not touch them.
func unlabeledState(t *testing.T, cli *client.Client) []string {
	t.Helper()
	ctx := context.Background()
	var out []string
	cs, err := cli.ContainerList(ctx, container.ListOptions{All: true})
	require.NoError(t, err)
	for _, c := range cs {
		if c.Labels[testLabelKey] != testLabelValue {
			out = append(out, "container:"+c.ID)
		}
	}
	imgs, err := cli.ImageList(ctx, image.ListOptions{All: true})
	require.NoError(t, err)
	for _, img := range imgs {
		if img.Labels[testLabelKey] != testLabelValue {
			out = append(out, "image:"+img.ID)
		}
	}
	vols, err := cli.VolumeList(ctx, volume.ListOptions{})
	require.NoError(t, err)
	for _, v := range vols.Volumes {
		if v.Labels[testLabelKey] != testLabelValue {
			out = append(out, "volume:"+v.Name)
		}
	}
	slices.Sort(out)
	return out
}

func buildImage(t *testing.T, cli *client.Client, tag, marker string) string {
	t.Helper()
	ctx := context.Background()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	// A Dockerfile with a single LABEL instruction makes a distinct image in
	// one step, so the build leaves no intermediate image behind. The test
	// label is part of that instruction: build time labels passed through
	// the API add a step of their own on the classic builder, whose
	// intermediate image would carry no test label.
	dockerfile := []byte("FROM busybox:latest\nLABEL reclaim.marker=" + marker + " " + testLabelKey + "=" + testLabelValue + "\n")
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "Dockerfile", Mode: 0o644, Size: int64(len(dockerfile))}))
	_, err := tw.Write(dockerfile)
	require.NoError(t, err)
	require.NoError(t, tw.Close())

	resp, err := cli.ImageBuild(ctx, &buf, build.ImageBuildOptions{
		Tags:        []string{tag},
		Remove:      true,
		ForceRemove: true,
	})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		var msg struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &msg) == nil && msg.Error != "" {
			t.Fatalf("build %s: %s", tag, msg.Error)
		}
	}
	require.NoError(t, sc.Err())
	img, err := cli.ImageInspect(ctx, tag)
	require.NoError(t, err)
	return img.ID
}

func TestDockerIntegration(t *testing.T) {
	cli := dockerClient(t)
	ctx := context.Background()
	labels := map[string]string{testLabelKey: testLabelValue}

	if _, err := cli.ImageInspect(ctx, "busybox:latest"); err != nil {
		rc, err := cli.ImagePull(ctx, "busybox:latest", image.PullOptions{})
		require.NoError(t, err)
		_, _ = io.Copy(io.Discard, rc)
		_ = rc.Close()
	}
	before := unlabeledState(t, cli)

	suffix := time.Now().Format("150405")
	usedImage := buildImage(t, cli, "reclaim-test-used:"+suffix, "used"+suffix)
	unusedImage := buildImage(t, cli, "reclaim-test-unused:"+suffix, "unused"+suffix)
	attachedVol := "reclaim-test-attached-" + suffix
	freeVol := "reclaim-test-free-" + suffix
	for _, name := range []string{attachedVol, freeVol} {
		_, err := cli.VolumeCreate(ctx, volume.CreateOptions{Name: name, Labels: labels})
		require.NoError(t, err)
	}
	stopped, err := cli.ContainerCreate(ctx,
		&container.Config{Image: usedImage, Cmd: []string{"true"}, Labels: labels},
		&container.HostConfig{Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: attachedVol, Target: "/data"}}},
		nil, nil, "reclaim-test-stopped-"+suffix)
	require.NoError(t, err)
	running, err := cli.ContainerCreate(ctx,
		&container.Config{Image: "busybox:latest", Cmd: []string{"sleep", "600"}, Labels: labels},
		nil, nil, nil, "reclaim-test-running-"+suffix)
	require.NoError(t, err)
	require.NoError(t, cli.ContainerStart(ctx, running.ID, container.StartOptions{}))

	t.Cleanup(func() {
		// Remove only what this test created, by id or name.
		for _, id := range []string{running.ID, stopped.ID} {
			_ = cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true})
		}
		for _, id := range []string{usedImage, unusedImage} {
			_, _ = cli.ImageRemove(ctx, id, image.RemoveOptions{Force: true, PruneChildren: true})
		}
		for _, name := range []string{attachedVol, freeVol} {
			_ = cli.VolumeRemove(ctx, name, true)
		}
	})

	a, out := testApp(t, "")
	a.docker = func() (dockerx.API, error) {
		c, err := dockerx.New(os.Getenv, a.home)
		if err != nil {
			return nil, err
		}
		return c, nil
	}
	work := t.TempDir()
	reportPath := filepath.Join(work, "report.json")
	require.NoError(t, execute(a, "scan", "--category", "docker", "--docker-label", testLabelKey+"="+testLabelValue,
		"--min-size", "0", "--json", "--out", reportPath))
	var report finding.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))

	got := map[string]finding.Tier{}
	for _, f := range report.Findings {
		got[f.Target] = f.Tier
	}
	require.Equal(t, map[string]finding.Tier{
		stopped.ID:  finding.TierB,
		unusedImage: finding.TierB,
		freeVol:     finding.TierB,
		attachedVol: finding.TierC,
	}, got, "exactly the labeled fixtures that nothing running uses")

	planPath := filepath.Join(work, "plan.json")
	require.NoError(t, execute(a, "select", "--preset", "aggressive", "--report", reportPath, "--out", planPath))
	p, err := plan.Load(planPath)
	require.NoError(t, err)
	require.Len(t, p.Actions, 3)
	require.NoError(t, execute(a, "apply", planPath, "--yes", "--log", filepath.Join(work, "apply.log")))

	_, err = cli.ContainerInspect(ctx, stopped.ID)
	require.True(t, dockerx.IsNotFound(err), "stopped container removed")
	_, err = cli.ImageInspect(ctx, unusedImage)
	require.True(t, dockerx.IsNotFound(err), "unused image removed")
	_, err = cli.VolumeInspect(ctx, freeVol)
	require.True(t, dockerx.IsNotFound(err), "free volume removed")

	_, err = cli.VolumeInspect(ctx, attachedVol)
	require.NoError(t, err, "tier C volume was not selected and must remain")
	_, err = cli.ImageInspect(ctx, usedImage)
	require.NoError(t, err, "image of a container is never removed")
	info, err := cli.ContainerInspect(ctx, running.ID)
	require.NoError(t, err)
	require.True(t, info.State.Running, "running container untouched")

	require.Equal(t, before, unlabeledState(t, cli), "nothing without the test label changed")
}
