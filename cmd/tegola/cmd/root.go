package cmd

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/alexeydott/tegola/atlas"
	"github.com/alexeydott/tegola/cmd/internal/register"
	cachecmd "github.com/alexeydott/tegola/cmd/tegola/cmd/cache"
	"github.com/alexeydott/tegola/config"
	"github.com/alexeydott/tegola/dict"
	"github.com/alexeydott/tegola/internal/build"
	"github.com/alexeydott/tegola/internal/log"
	"github.com/alexeydott/tegola/server"
	"github.com/go-spatial/cobra"
)

var (
	logLevel   string
	configFile string
	// parsed config
	conf config.Config

	// RequireCache in this instance
	RequireCache bool
)

func init() {
	// root
	RootCmd.PersistentFlags().StringVar(&configFile, "config", "config.toml",
		"path or http url to a config file, or \"-\" for stdin")
	RootCmd.PersistentFlags().StringVar(&logLevel, "log-level", "INFO",
		"set log level to: DEBUG, INFO, WARN, ERROR or SILENT")

	// server
	serverCmd.Flags().StringVarP(&serverPort, "port", "p", defaultHTTPPort, "port to bind tile server to")
	serverCmd.Flags().BoolVarP(&serverNoCache, "no-cache", "n", false, "turn off the cache")

	bindFeatureRuntime(RootCmd, serverCmd, initializeCommandRuntime, runServer)
	RootCmd.AddCommand(serverCmd)
	// cache seed / purge
	cachecmd.Config = &conf
	RootCmd.AddCommand(cachecmd.Cmd)
	// version
	RootCmd.AddCommand(versionCmd)
}

var RootCmd = &cobra.Command{
	Use:   "tegola",
	Short: "tegola is a vector tile server",
	Long: fmt.Sprintf(`tegola is a vector tile server
Version: %v`, build.Version),
	PersistentPreRunE: rootCmdValidatePersistent,
}

func rootCmdValidatePersistent(cmd *cobra.Command, _ []string) (err error) {
	requireCache := RequireCache || cachecmd.RequireCache
	cmdName := cmd.CalledAs()
	switch cmdName {
	case "help", "version":
		build.Commands = append(build.Commands, cmdName)
		return nil
	default:
		return initConfig(configFile, requireCache, logLevel)
	}
}

func initConfig(configFile string, cacheRequired bool, logLevel string) error {
	_, err := initConfigRuntime(configFile, cacheRequired, logLevel)
	return err
}

func initConfigRuntime(configFile string, cacheRequired bool, logLevel string) (api *server.FeatureAPI, err error) {
	// Parse the provided log level; default to INFO if parsing fails.
	lvl := log.ParseLogLevel(logLevel)

	logger := log.NewLogger(lvl).
		WithGroup("tegola").
		With("version", build.Version, "pid", os.Getpid(), "rev", build.GitRevision)

	// set out logger as the new default slog logger
	slog.SetDefault(logger)

	if conf, err = config.Load(configFile); err != nil {
		return nil, err
	}
	if err = conf.Validate(); err != nil {
		return nil, err
	}

	// init our providers
	// but first convert []env.Map -> []dict.Dicter
	provArr := make([]dict.Dicter, len(conf.Providers))
	for i := range provArr {
		provArr[i] = conf.Providers[i]
	}

	providers, err := register.Providers(provArr, conf.Maps)
	if err != nil {
		return nil, fmt.Errorf("could not register providers: %v", err)
	}

	service, err := register.Features(conf.Features, providers)
	if err != nil {
		return nil, fmt.Errorf("could not register features: %w", err)
	}
	if service != nil {
		settings := conf.Features.Resolved()
		api, err = server.NewFeatureAPI(service, server.FeatureAPIConfig{BasePath: string(settings.BasePath), DefaultLimit: uint(*settings.DefaultLimit), MaxLimit: uint(*settings.MaxLimit), Title: string(settings.Title), Description: string(settings.Description)})
		if err != nil {
			return nil, fmt.Errorf("could not construct feature runtime: %w", err)
		}
	}

	// init our maps
	if err = register.Maps(nil, conf.Maps, providers); err != nil {
		return nil, fmt.Errorf("could not register maps: %v", err)
	}
	if len(conf.Cache) == 0 && cacheRequired {
		return nil, fmt.Errorf("no cache defined in config, please check your config (%v)", configFile)
	}
	if serverNoCache {
		log.Info("Cache explicitly turned off by user via command line")
	} else if len(conf.Cache) > 0 {
		// init cache backends
		cache, err := register.Cache(conf.Cache)
		if err != nil {
			return nil, fmt.Errorf("could not register cache: %v", err)
		}
		if cache != nil {
			atlas.SetCache(cache)
		}
	}
	observer, err := register.Observer(conf.Observer)
	if err != nil {
		return nil, err
	}
	atlas.SetObservability(observer)
	return api, nil
}

// bindFeatureRuntime keeps publication state inside one command assembly.
// Clear it before initialization, including errors, so a reused command cannot
// serve a runtime retained from an earlier successful initialization.
func bindFeatureRuntime(root, serve *cobra.Command, initialize func(*cobra.Command, []string) (*server.FeatureAPI, error), run func(*cobra.Command, []string, *server.FeatureAPI) error) {
	var runtime *server.FeatureAPI
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		runtime = nil
		api, err := initialize(cmd, args)
		if err != nil {
			return err
		}
		runtime = api
		return nil
	}
	serve.Run = nil
	serve.RunE = func(cmd *cobra.Command, args []string) error { return run(cmd, args, runtime) }
}

func initializeCommandRuntime(cmd *cobra.Command, _ []string) (*server.FeatureAPI, error) {
	switch cmd.CalledAs() {
	case "help", "version":
		build.Commands = append(build.Commands, cmd.CalledAs())
		return nil, nil
	default:
		return initConfigRuntime(configFile, RequireCache || cachecmd.RequireCache, logLevel)
	}
}
