# The PDF converter: Chromium behind an HTTP API, with Indic fonts.
#
# Gotenberg is Chromium in a container with a conversion endpoint in front of
# it. The backend stays a distroless static binary and posts HTML here, which
# is the only way to keep both properties that matter: a small, dependency-free
# API image, and text shaping good enough for Devanagari.
#
# The base image carries a Latin font set. Marathi needs more than a font --
# it needs the shaping engine to have glyphs to shape -- so the Noto Devanagari
# faces are installed explicitly rather than hoped for. Without them Chromium
# falls back, and a fallback font with no Devanagari coverage renders a
# farmer's report as empty boxes.
FROM gotenberg/gotenberg:8

USER root

RUN apt-get update \
 && apt-get install --no-install-recommends -y \
      fonts-noto-core \
      fonts-noto-ui-core \
      fonts-deva \
      fonts-indic \
 && fc-cache -f \
 && rm -rf /var/lib/apt/lists/*

USER gotenberg
