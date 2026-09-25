package cmd

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/go-spatial/cobra"
	"github.com/go-spatial/tegola/atlas"
	"github.com/go-spatial/tegola/internal/build"
	gdcmd "github.com/go-spatial/tegola/internal/cmd"
	"github.com/go-spatial/tegola/internal/log"
	"github.com/go-spatial/tegola/observability"
	"github.com/go-spatial/tegola/provider"
	"github.com/go-spatial/tegola/server"
)

var (
	serverPort      string
	serverNoCache   bool
	defaultHTTPPort = ":8080"
)

var serverCmd = &cobra.Command{
	Use:     "serve",
	Short:   "Use tegola as a tile server",
	Aliases: []string{"server"},
	Long:    `Use tegola as a vector tile server. Maps tiles will be served at /maps/:map_name/:z/:x/:y`,
	Run: func(cmd *cobra.Command, args []string) {
		gdcmd.New()
		gdcmd.OnComplete(provider.Cleanup)
		gdcmd.OnComplete(observability.Cleanup)

		// Resolve the listen port. An explicitly-passed --port flag always
		// overrides the config value; when the flag is not set we fall back to the
		// config value, then to the flag default. (Comparing against the default
		// string is not enough because an explicit ":8080" is indistinguishable
		// from unset, so use the flag's IsSet semantics instead.)
		serverPort = resolveServerPort(cmd.Flags().Changed("port"), serverPort, string(conf.Webserver.Port))

		if conf.Webserver.HostName.Host != "" {
			u := url.URL(conf.Webserver.HostName)
			server.HostName = &u
		}

		// set our server version
		server.Version = build.Version
		build.Commands = append(build.Commands, cmd.Name())
		atlas.StartSubProcesses()

		// set user defined response headers
		for name, value := range conf.Webserver.Headers {
			// cast to string
			val := fmt.Sprintf("%v", value)
			// check that we have a value set
			if val == "" {
				log.Errorf("webserver.header (%v) has no configured value", val)
				os.Exit(1)
			}

			server.Headers[name] = val
		}

		if conf.Webserver.URIPrefix != "" {
			server.URIPrefix = string(conf.Webserver.URIPrefix)
		}

		if conf.Webserver.ProxyProtocol != "" {
			server.ProxyProtocol = string(conf.Webserver.ProxyProtocol)
		}

		if conf.Webserver.SSLCert+conf.Webserver.SSLKey != "" {
			if conf.Webserver.SSLCert == "" {
				// error
				log.Error("config must have both or nether ssl_key and ssl_cert, missing ssl_cert")
				os.Exit(1)
			}

			if conf.Webserver.SSLKey == "" {
				// error
				log.Error("config must have both or nether ssl_key and ssl_cert, missing ssl_key")
				os.Exit(1)
			}

			server.SSLCert = string(conf.Webserver.SSLCert)
			server.SSLKey = string(conf.Webserver.SSLKey)
		}

		// start our webserver
		srv := server.Start(nil, serverPort)
		shutdown(srv)
		<-gdcmd.Cancelled()
		gdcmd.Complete()
	},
}

// resolveServerPort returns the port the HTTP server should bind to. An
// explicitly-set CLI --port (flagSet true) always overrides the config value;
// otherwise the config value is used when present, falling back to flagPort
// (the flag's value or its default).
func resolveServerPort(flagSet bool, flagPort, configPort string) string {
	if flagSet {
		return flagPort
	}
	if configPort != "" {
		return configPort
	}
	return flagPort
}

func shutdown(srv *http.Server) {
	gdcmd.OnComplete(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel() // releases resources if slowOperation completes before timeout elapses
		_ = srv.Shutdown(ctx)
	})
}
