package build

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/containerd/platforms"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/frontend/dockerui"
	"github.com/moby/buildkit/frontend/gateway/client"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

// Builder contains the Build function we pass to buildkit
type Builder struct {
}

// New creates a new builder with the appropriate converter
func New() Builder {
	return Builder{}
}

// Build runs the build
func (b Builder) Build(ctx context.Context, c client.Client) (*client.Result, error) {
	return BuildWithNix(ctx, c)
}

// mimic dockerfile.v1 frontend
const (
	localNameContext     = "context"
	localNameDockerfile  = "dockerfile"
	dockerignoreFilename = ".dockerignore"
	keyFilename          = "filename"
	keyTargetName        = "target"
)

type LayerResult struct {
	State llb.State
	Info  Layer
}

func BuildWithNix(ctx context.Context, c client.Client) (*client.Result, error) {
	nixBuilder := GetBuilder(ctx, c)
	buildResult, err := Nix2ContainerBuild(nixBuilder, ctx, c)
	if err != nil {
		return nil, errors.Wrap(err, "failed to run nix2container build")
	}
	config, err := ParseNix2ContainerConfig(buildResult, ctx)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse nix2container config")
	}
	layersSt, err := ConstructLayers(nixBuilder, buildResult, *config, ctx, c)
	if err != nil {
		return nil, errors.Wrap(err, "failed to construct layers")
	}
	finalResult, err := CombineLayers(layersSt, ctx, c)
	if err != nil {
		return nil, errors.Wrap(err, "failed to combine layers")
	}
	image, err := ExportImage(finalResult, *config, ctx, c)
	if err != nil {
		return nil, errors.Wrap(err, "failed to export image")
	}
	return image, nil
}

func GetBuilder(ctx context.Context, c client.Client) llb.State {
	return llb.Image("nixos/nix:2.24.11").
		AddEnv("PATH", "/bin:/usr/bin:/nix/var/nix/profiles/default/bin").
		File(llb.Mkdir("/out", 0755, llb.WithParents(true)))
}

func GetSelfImage(ctx context.Context, c client.Client) llb.State {
	return llb.Image(os.Getenv("BUILDER_TAG"), llb.ResolveDigest(true))
}

func Nix2ContainerBuild(nixBuilder llb.State, ctx context.Context, c client.Client) (*client.Result, error) {
	excludePatterns, err := getExcludes(ctx, c)
	if err != nil {
		return nil, err
	}

	localCtxSt := llb.Local(localNameContext,
		llb.SessionID(c.BuildOpts().SessionID),
		llb.ExcludePatterns(excludePatterns),
		dockerui.WithInternalName("local context"),
	)

	targetName := c.BuildOpts().Opts[keyTargetName]
	flakePath := "."
	if targetName != "" {
		flakePath = fmt.Sprintf("%s#%s", flakePath, targetName)
	}

	buildSt := nixBuilder.Run(
		llb.AddMount("/context", localCtxSt),
		llb.AddMount("/nix", nixBuilder, llb.SourcePath("/nix"), llb.AsPersistentCacheDir("nix2container-buildkit-nix-cache", llb.CacheMountShared)),
		llb.AddMount("/root/cache", llb.Scratch(), llb.AsPersistentCacheDir("nix2container-buildkit-cache", llb.CacheMountShared)),
		llb.Dir("/context"),
		llb.Shlexf("bash -c \"nix -L --extra-experimental-features 'nix-command flakes' build --max-jobs auto --accept-flake-config --option build-users-group '' -o /out/image-link.json %s && cat /out/image-link.json > /out/image.json\"", flakePath),
		llb.WithCustomNamef("running nix2container build"),
	)

	// Get build result
	def, err := buildSt.Marshal(ctx)
	if err != nil {
		return nil, err
	}

	buildResult, err := c.Solve(ctx, client.SolveRequest{
		Definition: def.ToPB(),
	})
	return buildResult, err
}

func ParseNix2ContainerConfig(buildResult *client.Result, ctx context.Context) (config *Nix2ContainerConfig, err error) {
	// Read and parse the image config
	configBytes, err := buildResult.Ref.ReadFile(ctx, client.ReadRequest{
		Filename: "/out/image.json",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to read image config: %w", err)
	}

	if err := json.Unmarshal(configBytes, &config); err != nil {
		return nil, fmt.Errorf("failed to parse image config: %w", err)
	}
	return config, nil
}

func ConstructLayers(nixBuilder llb.State, buildResult *client.Result, config Nix2ContainerConfig, ctx context.Context, c client.Client) ([]LayerResult, error) {
	buildResultSt, err := buildResult.Ref.ToState()
	if err != nil {
		return nil, fmt.Errorf("failed to convert build result to state: %w", err)
	}

	selfImageSt := GetSelfImage(ctx, c)

	// Create layersSt for each store path
	var layersSt []LayerResult
	for _, layer := range config.Layers {
		// Construct layer from paths
		emptyLayer := llb.Scratch().
			File(llb.Mkdir("/layer/nix/store", 0755, llb.WithParents(true)))
		constructLayerExec := emptyLayer.
			Run(
				llb.AddMount("/src", nixBuilder, llb.AsPersistentCacheDir("nix2container-buildkit-nix-cache", llb.CacheMountShared)),
				llb.AddMount("/build", buildResultSt),
				llb.AddMount("/self", selfImageSt),
				llb.Shlexf("/self/construct-layer --config %s --digest %s --source-prefix %s --dest-prefix %s", "/build/out/image.json", layer.Digest, "/src", "/layer"),
				llb.WithCustomNamef("constructing layer %s", layer.Digest),
			)

		constructLayerDef, err := constructLayerExec.Marshal(ctx)
		if err != nil {
			return nil, err
		}
		constructLayerResult, err := c.Solve(ctx, client.SolveRequest{
			Definition: constructLayerDef.ToPB(),
		})
		if err != nil {
			return nil, err
		}

		layerState, err := constructLayerResult.Ref.ToState()
		if err != nil {
			return nil, err
		}

		layersSt = append(layersSt, LayerResult{
			State: layerState,
			Info:  layer,
		})
	}

	return layersSt, nil
}

func CombineLayers(layers []LayerResult, ctx context.Context, c client.Client) (*client.Result, error) {
	// Create final state by copying layers
	finalState := llb.Scratch()
	for _, layer := range layers {
		finalState = finalState.File(
			llb.Copy(layer.State, "/layer", "/", &llb.CopyInfo{
				CopyDirContentsOnly: true,
			}),
			llb.WithCustomNamef("copy layer %s", layer.Info.Digest),
		)
	}

	// Create final result
	finalDef, err := finalState.Marshal(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "failed to marshal final state")
	}
	finalResult, err := c.Solve(ctx, client.SolveRequest{
		Definition: finalDef.ToPB(),
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to solve final state")
	}

	return finalResult, nil
}

func ExportImage(finalResult *client.Result, config Nix2ContainerConfig, ctx context.Context, c client.Client) (*client.Result, error) {
	res := client.NewResult()
	ref, err := finalResult.SingleRef()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get single ref")
	}

	platform := platforms.DefaultSpec()

	// Create image configuration
	ImageConfig := config.ImageConfig
	image := specs.Image{
		Config: ImageConfig,
		RootFS: specs.RootFS{
			Type: "layers",
		},
		Platform: platform,
	}
	specJson, err := json.Marshal(image)
	if err != nil {
		return nil, errors.Wrap(err, "failed to marshal image config")
	}
	res.SetRef(ref)
	res.AddMeta(exptypes.ExporterImageConfigKey, specJson)

	return res, nil
}
