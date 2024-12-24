package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"

	"github.com/arahizzz/nix2container-buildkit/internal/build"
	"github.com/sirupsen/logrus"
)

var (
	configFlag = flag.String("config", "", "path to image config file")
	digestFlag = flag.String("digest", "", "digest of the layer to construct")
	srcPrefix  = flag.String("source-prefix", "", "prefix to add to paths")
	destPrefix = flag.String("dest-prefix", "", "prefix to add to paths")
)

func main() {
	if err := runCommand(); err != nil {
		logrus.Errorf("fatal error: %+v", err)
		panic(err)
	}
}

func runCommand() error {
	flag.Parse()

	if configFlag == nil || *configFlag == "" {
		return errors.New("config flag is required")
	}

	if digestFlag == nil || *digestFlag == "" {
		return errors.New("digest flag is required")
	}

	if srcPrefix == nil || *srcPrefix == "" {
		defaultSrcPrefix := "/src"
		srcPrefix = &defaultSrcPrefix
	}

	if destPrefix == nil || *destPrefix == "" {
		defaultDestPrefix := "/"
		destPrefix = &defaultDestPrefix
	}

	config, err := loadConfig(*configFlag)
	if err != nil {
		return err
	}

	return constructLayer(config)
}

func loadConfig(path string) (*build.Nix2ContainerConfig, error) {
	logrus.Infof("loading config from %s", path)
	buff, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	config := &build.Nix2ContainerConfig{}
	err = json.Unmarshal(buff, config)
	return config, err
}

func constructLayer(config *build.Nix2ContainerConfig) error {
	logrus.Infof("constructing layer %s", *digestFlag)
	var layer build.Layer
	for _, l := range config.Layers {
		if l.Digest == *digestFlag {
			layer = l
		}
	}

	for _, path := range layer.Paths {
		srcPath := *srcPrefix + path.Path
		destPath := *destPrefix + path.Path

		if path.Options != nil && path.Options.Rewrite != nil {
			destPath = *destPrefix + regexp.MustCompile(path.Options.Rewrite.Regex).
				ReplaceAllString(path.Path, path.Options.Rewrite.Repl)
			srcPath = srcPath + "/*"
		}

		logrus.Infof("copying %s to %s", srcPath, destPath)
		cmdStr := fmt.Sprintf("/self/bin/cp -a %[1]s %[2]s", srcPath, destPath)
		cmd := exec.Command("/self/bin/sh", "-c", cmdStr)
		out, err := cmd.CombinedOutput()
		logrus.Infof("%s", string(out))

		if err != nil {
			return err
		}
	}

	return nil
}
