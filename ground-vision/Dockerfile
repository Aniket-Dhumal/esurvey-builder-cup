# Ground computer vision for the assessment platform.
#
# Build context is this directory:
#
#   docker build -f ground-vision/Dockerfile ground-vision

# --- build -------------------------------------------------------------------
FROM python:3.13-slim AS build

COPY --from=ghcr.io/astral-sh/uv:0.11.0 /uv /usr/local/bin/uv

ENV UV_COMPILE_BYTECODE=1 \
    UV_LINK_MODE=copy \
    UV_PYTHON_DOWNLOADS=never

WORKDIR /src

# Dependencies before source, so editing a colour threshold does not reinstall
# OpenCV and NumPy.
COPY pyproject.toml ./
RUN uv venv /opt/venv && \
    VIRTUAL_ENV=/opt/venv uv pip install --no-cache .

COPY src/ ./src/
RUN VIRTUAL_ENV=/opt/venv uv pip install --no-cache --no-deps .

# --- runtime -----------------------------------------------------------------
FROM python:3.13-slim

# OpenCV's headless wheel still links libGL and libglib for its image codecs.
RUN apt-get update && \
    apt-get install -y --no-install-recommends libgl1 libglib2.0-0 && \
    rm -rf /var/lib/apt/lists/*

RUN groupadd --system --gid 10001 esurvey && \
    useradd --system --uid 10001 --gid esurvey --no-create-home esurvey

COPY --from=build /opt/venv /opt/venv

ENV PATH="/opt/venv/bin:$PATH" \
    PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    ESURVEY_VISION_HOST=0.0.0.0 \
    ESURVEY_VISION_PORT=8300

WORKDIR /app
USER esurvey:esurvey

EXPOSE 8300

# Liveness only, like the radar service. A probe that failed when measuring was
# switched off would restart a healthy container in a loop.
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD python -c "import urllib.request,sys; \
sys.exit(0 if urllib.request.urlopen('http://127.0.0.1:8300/healthz', timeout=3).status==200 else 1)"

CMD ["python", "-m", "esurvey_vision.main"]
