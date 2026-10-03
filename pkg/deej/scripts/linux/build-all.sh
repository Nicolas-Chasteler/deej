#!/bin/sh

echo 'Building deej (all)...'

# the build scripts find the repo root themselves, so only their own path
# matters here
"$(dirname "$0")/build-dev.sh" || exit 1
"$(dirname "$0")/build-release.sh"
