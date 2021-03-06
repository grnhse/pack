package testhelpers

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dgodd/dockerdial"
	dockertypes "github.com/docker/docker/api/types"
	"github.com/google/go-cmp/cmp"

	"github.com/buildpack/pack/archive"
	"github.com/buildpack/pack/docker"
)

func RandString(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a' + byte(rand.Intn(26))
	}
	return string(b)
}

// Assert deep equality (and provide useful difference as a test failure)
func AssertEq(t *testing.T, actual, expected interface{}) {
	t.Helper()
	if diff := cmp.Diff(actual, expected); diff != "" {
		t.Fatal(diff)
	}
}

// Assert the simplistic pointer (or literal value) equality
func AssertSameInstance(t *testing.T, actual, expected interface{}) {
	t.Helper()
	if actual != expected {
		t.Fatalf("Expected %s and %s to be pointers to the variable", actual, expected)
	}
}

func AssertError(t *testing.T, actual error, expected string) {
	t.Helper()
	if actual == nil {
		t.Fatalf("Expected an error but got nil")
	}
	if !strings.Contains(actual.Error(), expected) {
		t.Fatalf(`Expected error to contain "%s", got "%s"`, expected, actual.Error())
	}
}

func AssertContains(t *testing.T, actual, expected string) {
	t.Helper()
	if !strings.Contains(actual, expected) {
		t.Fatalf("Expected: '%s' to contain '%s'", actual, expected)
	}
}

func AssertContainsMatch(t *testing.T, actual, exp string) {
	t.Helper()
	regex := regexp.MustCompile(exp)
	matches := regex.FindAll([]byte(actual), -1)
	if len(matches) < 1 {
		t.Fatalf("Expected: '%s' to match expression '%s'", actual, exp)
	}
}

func AssertNotContains(t *testing.T, actual, expected string) {
	t.Helper()
	if strings.Contains(actual, expected) {
		t.Fatalf("Expected: '%s' to not contain '%s'", actual, expected)
	}
}

func AssertSliceContains(t *testing.T, slice []string, value string) {
	t.Helper()
	for _, s := range slice {
		if value == s {
			return
		}
	}
	t.Fatalf("Expected: '%s' to contain element '%s'", slice, value)
}

func AssertMatch(t *testing.T, actual string, expected string) {
	t.Helper()
	if !regexp.MustCompile(expected).MatchString(actual) {
		t.Fatalf("Expected: '%s' to match regex '%s'", actual, expected)
	}
}

func AssertNil(t *testing.T, actual interface{}) {
	t.Helper()
	if !isNil(actual) {
		t.Fatalf("Expected nil: %s", actual)
	}
}

func AssertNotNil(t *testing.T, actual interface{}) {
	t.Helper()
	if isNil(actual) {
		t.Fatal("Expected not nil")
	}
}

func isNil(value interface{}) bool {
	return value == nil || (reflect.TypeOf(value).Kind() == reflect.Ptr && reflect.ValueOf(value).IsNil())
}

func AssertNotEq(t *testing.T, actual, expected interface{}) {
	t.Helper()
	if diff := cmp.Diff(actual, expected); diff == "" {
		t.Fatalf("Expected values to differ: %s", actual)
	}
}

func AssertDirContainsFileWithContents(t *testing.T, dir string, file string, expected string) {
	t.Helper()
	path := filepath.Join(dir, file)
	bytes, err := ioutil.ReadFile(path)
	AssertNil(t, err)
	if string(bytes) != expected {
		t.Fatalf("file %s in dir %s has wrong contents: %s != %s", file, dir, string(bytes), expected)
	}
}

var dockerCliVal *docker.Client
var dockerCliOnce sync.Once
var dockerCliErr error

func dockerCli(t *testing.T) *docker.Client {
	dockerCliOnce.Do(func() {
		dockerCliVal, dockerCliErr = docker.New()
	})
	AssertNil(t, dockerCliErr)
	return dockerCliVal
}

func proxyDockerHostPort(dockerCli *docker.Client, port string) error {
	ln, err := net.Listen("tcp", ":"+port)
	if err != nil {
		return err
	}

	go func() {
		// TODO exit somehow.
		for {
			conn, err := ln.Accept()
			if err != nil {
				log.Println(err)
				continue
			}
			go func(conn net.Conn) {
				defer conn.Close()
				c, err := dockerdial.Dial("tcp", "localhost:"+port)
				if err != nil {
					log.Println(err)
					return
				}
				defer c.Close()

				go io.Copy(c, conn)
				io.Copy(conn, c)
			}(conn)
		}
	}()
	return nil
}

func Eventually(t *testing.T, test func() bool, every time.Duration, timeout time.Duration) {
	t.Helper()

	ticker := time.NewTicker(every)
	defer ticker.Stop()
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case <-ticker.C:
			if test() {
				return
			}
		case <-timer.C:
			t.Fatalf("timeout on eventually: %v", timeout)
		}
	}
}

func ConfigurePackHome(t *testing.T, packHome, registryPort string) {
	t.Helper()
	tag := PackTag()
	AssertNil(t, ioutil.WriteFile(filepath.Join(packHome, "config.toml"), []byte(fmt.Sprintf(`
				default-stack-id = "io.buildpacks.stacks.bionic"
                default-builder = "%s"

				[[stacks]]
				  id = "io.buildpacks.stacks.bionic"
				  build-image = "%s"
				  run-images = ["%s"]

                [[run-images]]
                  image = "packs/run:%s"
                  mirrors = ["%s"]
			`, DefaultBuilderImage(t, registryPort), DefaultBuildImage(t, registryPort), DefaultRunImage(t, registryPort), tag, DefaultRunImage(t, registryPort))), 0666))
}

func CreateImageOnLocal(t *testing.T, dockerCli *docker.Client, repoName, dockerFile string) {
	ctx := context.Background()

	buildContext, err := archive.CreateSingleFileTarReader("Dockerfile", dockerFile)
	AssertNil(t, err)

	res, err := dockerCli.ImageBuild(ctx, buildContext, dockertypes.ImageBuildOptions{
		Tags:           []string{repoName},
		SuppressOutput: true,
		Remove:         true,
		ForceRemove:    true,
	})
	AssertNil(t, err)

	io.Copy(ioutil.Discard, res.Body)
	res.Body.Close()
}

func CreateImageOnRemote(t *testing.T, dockerCli *docker.Client, registryConfig *TestRegistryConfig, repoName, dockerFile string) string {
	t.Helper()
	imageName := registryConfig.RepoName(repoName)

	defer DockerRmi(dockerCli, imageName)
	CreateImageOnLocal(t, dockerCli, imageName, dockerFile)
	AssertNil(t, PushImage(dockerCli, imageName, registryConfig))
	return imageName
}

func DockerRmi(dockerCli *docker.Client, repoNames ...string) error {
	var err error
	ctx := context.Background()
	for _, name := range repoNames {
		_, e := dockerCli.ImageRemove(
			ctx,
			name,
			dockertypes.ImageRemoveOptions{Force: true, PruneChildren: true},
		)
		if e != nil && err == nil {
			err = e
		}
	}
	return err
}

func PushImage(dockerCli *docker.Client, ref string, registryConfig *TestRegistryConfig) error {
	rc, err := dockerCli.ImagePush(context.Background(), ref, dockertypes.ImagePushOptions{RegistryAuth: registryConfig.RegistryAuth()})
	if err != nil {
		return err
	}
	if _, err := io.Copy(ioutil.Discard, rc); err != nil {
		return err
	}
	return rc.Close()
}

const DefaultTag = "rc"

func PackTag() string {
	tag := os.Getenv("PACK_TAG")
	if tag == "" {
		return DefaultTag
	}
	return tag
}

func HttpGet(t *testing.T, url string) string {
	t.Helper()
	txt, err := HttpGetE(url, map[string]string{})
	AssertNil(t, err)
	return txt
}

func HttpGetE(url string, headers map[string]string) (string, error) {
	var client *http.Client
	if os.Getenv("DOCKER_HOST") == "" {
		client = http.DefaultClient
	} else {
		tr := &http.Transport{Dial: dockerdial.Dial}
		client = &http.Client{Transport: tr}
	}

	request, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}

	for key, val := range headers {
		request.Header.Set(key, val)
	}

	resp, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP Status was bad: %s => %d", url, resp.StatusCode)
	}
	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func ImageID(t *testing.T, repoName string) string {
	t.Helper()
	inspect, _, err := dockerCli(t).ImageInspectWithRaw(context.Background(), repoName)
	AssertNil(t, err)
	return inspect.ID
}

func Run(t *testing.T, cmd *exec.Cmd) string {
	t.Helper()
	txt, err := RunE(cmd)
	AssertNil(t, err)
	return txt
}

func CleanDefaultImages(t *testing.T, registryPort string) {
	t.Helper()
	AssertNil(t,
		DockerRmi(
			dockerCli(t),
			DefaultRunImage(t, registryPort),
			DefaultBuildImage(t, registryPort),
			DefaultBuilderImage(t, registryPort),
		),
	)
}

func RunE(cmd *exec.Cmd) (string, error) {
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("Failed to execute command: %v, %s, %s", cmd.Args, err, output)
	}

	return string(output), nil
}

func TryPullImage(dockerCli *docker.Client, ref string) error {
	return PullImageWithAuth(dockerCli, ref, "")
}

func PullImageWithAuth(dockerCli *docker.Client, ref, registryAuth string) error {
	rc, err := dockerCli.ImagePull(context.Background(), ref, dockertypes.ImagePullOptions{RegistryAuth: registryAuth})
	if err != nil {
		return nil
	}
	if _, err := io.Copy(ioutil.Discard, rc); err != nil {
		return err
	}
	return rc.Close()
}

func RecursiveCopy(t *testing.T, src, dst string) {
	t.Helper()
	fis, err := ioutil.ReadDir(src)
	AssertNil(t, err)
	for _, fi := range fis {
		if fi.Mode().IsRegular() {
			srcFile, err := os.Open(filepath.Join(src, fi.Name()))
			AssertNil(t, err)
			dstFile, err := os.OpenFile(filepath.Join(dst, fi.Name()), os.O_RDWR|os.O_CREATE|os.O_TRUNC, fi.Mode())
			AssertNil(t, err)
			_, err = io.Copy(dstFile, srcFile)
			AssertNil(t, err)
			modifiedtime := time.Time{}
			err = os.Chtimes(filepath.Join(dst, fi.Name()), modifiedtime, modifiedtime)
			AssertNil(t, err)
		}
		if fi.IsDir() {
			err = os.Mkdir(filepath.Join(dst, fi.Name()), fi.Mode())
			AssertNil(t, err)
			RecursiveCopy(t, filepath.Join(src, fi.Name()), filepath.Join(dst, fi.Name()))
		}
	}
	modifiedtime := time.Time{}
	err = os.Chtimes(dst, modifiedtime, modifiedtime)
	AssertNil(t, err)
	err = os.Chmod(dst, 0775)
	AssertNil(t, err)
}

func UntarSingleFile(r io.Reader, fileName string) ([]byte, error) {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			// end of tar archive
			return []byte{}, fmt.Errorf("file '%s' does not exist in tar", fileName)
		}
		if err != nil {
			return []byte{}, err
		}

		switch hdr.Typeflag {
		case tar.TypeReg, tar.TypeRegA:
			if hdr.Name == fileName {
				return ioutil.ReadAll(tr)
			}
		}
	}
}

func RequireDocker(t *testing.T) {
	_, ok := os.LookupEnv("NO_DOCKER")
	if ok {
		t.Skip("Skipping because docker daemon unavailable")
	}
}
