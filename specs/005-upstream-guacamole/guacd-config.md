---
title: guacd configuration and connection parameters
status: validated
track: server
depends_on:
  - specs/005-upstream-guacamole/guacd-compose-service.md
affects:
  - internal/config/config.go
  - internal/config/config_test.go
  - conf.yaml
  - docker/occamy.yaml
  - docker/docker-compose.yaml
effort: small
created: 2026-10-29
updated: 2026-10-29
author: Changkun Ou
dispatched_task_id: null
---

# guacd configuration and connection parameters

## Goal

Give `occamyd` the configuration it needs to reach guacd, and one function
that turns a token into guacd connection parameters, so that
[guacd-sessions](guacd-sessions.md) only has to wire them in.

## What to do

1. `internal/config/config.go`: add

   ```go
   type Guacd struct {
       Address string                       `yaml:"address"`
       Params  map[string]map[string]string `yaml:"params"`
   }
   ```

   as `config.Guacd`, defaulting `Address` to `localhost:4822` when unset.
2. `conf.yaml`:

   ```yaml
   guacd:
     address: localhost:4822
     params:            # per-protocol defaults, merged under token values
       rdp:
         ignore-cert: "true"
   ```

3. Add `func (j *JWT) GuacdParams(defaults map[string]map[string]string) (map[string]string, error)`.
   It splits `j.Host` with `net.SplitHostPort` into `hostname` and `port`
   (returning the split error, as `lib.NewUser` does today), sets
   `username` and `password` from the claims, then copies
   `defaults[j.Protocol]` for names not already set.
4. `docker/occamy.yaml`: a copy of `conf.yaml` with `address: guacd:4822`.
   Mount it read-only over `/occamy/conf.yaml` in the `occamy` service of
   `docker/docker-compose.yaml`.

## Tests

In `internal/config/config_test.go`:

- `TestGuacdParams`: token values win over defaults, defaults fill the
  rest, and another protocol's defaults are not applied.
- `TestGuacdParamsMissingPort`: a host without a port returns an error.
- `TestGuacdDefaultAddress`: an empty `guacd` section yields
  `localhost:4822`.

## Boundaries

- Nothing reads the new config yet; `server/` is unchanged.
- No changes to JWT claims or `/api/v1/login`.
