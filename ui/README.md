# ui

The client side code for tegola's internal viewer. This codebase is built using Vue 3 and [MapLibre GL JS](https://maplibre.org/) v5, and uses [vite](https://vite.dev/) for building. The following npm commands can be used for basic operations:

## Project setup

```shell
npm ci --ignore-scripts --no-audit --no-fund
```

Note: package install scripts are denied by default (`.npmrc`, supply-chain
hardening). This tree builds without any install scripts; to approve one,
list the package in `.npmrc`'s `allow-scripts`.

### Compiles and hot-reloads for development

```shell
npm run dev
```

### Compiles and minifies for production

```shell
npm run build
```

## Building for inclusion in tegola

Build the locked assets from the `ui` folder before compiling Tegola:

```shell
npm ci --ignore-scripts --no-audit --no-fund
npm run build
git restore -- dist/.keep
```


`ui/embed.go` embeds `dist` at Go build time. The legacy `go run build.go`
helper also updates the browserslist database and can change lockfiles; use
the commands above for a reproducible release build.
