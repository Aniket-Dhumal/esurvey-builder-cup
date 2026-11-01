# Sentinel-1 backscatter for the assessment platform.
#
# Build context is this directory:
#
#   docker build -f satellite-intelligence/Dockerfile satellite-intelligence

# --- build -------------------------------------------------------------------
FROM python:3.15-slim AS build

COPY --from=ghcr.io/astral-sh/uv:0.11.0 /uv /usr/local/bin/uv

ENV UV_COMPILE_BYTECODE=1 \
    UV_LINK_MODE=copy \
    UV_PYTHON_DOWNLOADS=never

WORKDIR /src

# Dependencies before source, so editing a threshold does not reinstall the
# Earth Engine SDK and its transitive tree.
COPY pyproject.toml ./
RUN uv venv /opt/venv && \
    VIRTUAL_ENV=/opt/venv uv pip install --no-cache .

COPY src/ ./src/
RUN VIRTUAL_ENV=/opt/venv uv pip install --no-cache --no-deps .

# --- runtime -----------------------------------------------------------------
FROM python:3.15-slim

RUN groupadd --system --gid 10001 esurvey && \
    useradd --system --uid 10001 --gid esurvey --no-create-home esurvey

COPY --from=build /opt/venv /opt/venv

ENV PATH="/opt/venv/bin:$PATH" \
    PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    ESURVEY_SAR_HOST=0.0.0.0 \
    ESURVEY_SAR_PORT=8200 \
    # Where Application Default Credentials are mounted. Always set; the file
    # is only there when GOOGLE_ADC_FILE points at one, so a deployment
    # without Earth Engine is unaffected and answers 404 on submit.
    GOOGLE_APPLICATION_CREDENTIALS=/var/run/gcp/adc.json

WORKDIR /app
USER esurvey:esurvey

EXPOSE 8200

# Liveness only. A probe that failed when Earth Engine was down would restart
# a healthy container in a loop, which is the failure this separation exists
# to avoid.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD python -c "import urllib.request,sys; \
sys.exit(0 if urllib.request.urlopen('http://127.0.0.1:8200/healthz', timeout=3).status==200 else 1)"

CMD ["python", "-m", "esurvey_sar.main"]
