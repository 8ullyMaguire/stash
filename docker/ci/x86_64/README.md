This Dockerfile is used by CI to build the `stashapp/stash` Docker image. It must be run after cross-compiling - that is, `stash-linux` must exist in the `dist` directory. This image must be built from the `dist` directory.

Because the build context is `dist/`, anything else the image needs has to be
placed there first. `entrypoint.sh` is copied in by the "Collect binaries" step
in `.github/workflows/build.yml`; without that the `COPY` below has no source and
the release build fails.
