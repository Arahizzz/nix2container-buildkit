package main

/**
 * Utility script to construct based on it's JSON definition by copying files from nix cache into layer storage
 */

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
	srcPrefix  = flag.String("source-prefix", "", "source root prefix")
	destPrefix = flag.String("dest-prefix", "", "destination root prefix")
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

	return constructLayer(*config)
}

func loadConfig(path string) (*build.Layer, error) {
	logrus.Infof("loading config from %s", path)
	buff, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	config := &build.Layer{}
	err = json.Unmarshal(buff, config)
	return config, err
}

func constructLayer(layer build.Layer) error {
	logrus.Infof("constructing layer %s", layer.Digest)

	if layer.LayerPath == nil {
		return copyLayerPaths(layer)
	} else {
		return unpackLayer(layer)
	}
}

func copyLayerPaths(layer build.Layer) error {
	for _, path := range layer.Paths {
		srcPath := *srcPrefix + path.Path
		destPath := *destPrefix + path.Path

		if path.Options != nil && path.Options.Rewrite != nil {
			// If the rewrite option is set, adjust the destination path using the regex
			destPath = *destPrefix + regexp.MustCompile(path.Options.Rewrite.Regex).
				ReplaceAllString(path.Path, path.Options.Rewrite.Repl)
		    // Fixup src path, otherwise copy will end up incorrect
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

// Unpack non-reproducible layer from tar archive into destination
func unpackLayer(layer build.Layer) error {
	srcPath := *srcPrefix + *layer.LayerPath
	destPath := *destPrefix

	logrus.Infof("unpacking %s to %s", srcPath, destPath)
	cmdStr := fmt.Sprintf("/self/bin/tar -xf %[1]s -C %[2]s", srcPath, destPath)
	cmd := exec.Command("/self/bin/sh", "-c", cmdStr)
	out, err := cmd.CombinedOutput()
	logrus.Infof("%s", string(out))

	if err != nil {
		return err
	}

	return nil
}
