# ui

The client side code for tegola's internal viewer. This codebase is built using Vue 3 and [MapLibre GL JS](https://maplibre.org/) v5, and uses [vite](https://vite.dev/) for building. The following npm commands can be used for basic operations:

## Project setup

```shell
npm install
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

In order to compile the UI for inclusion in tegola, run the following commands from the `ui` folder:

```shell
go run build.go
```

