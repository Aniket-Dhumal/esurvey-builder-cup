# The Gemini assessment agent.
#
# Build context is this directory:
#
#   docker build -f ai-services/Dockerfile ai-services

# --- build -------------------------------------------------------------------
FROM python:3.13-slim AS build

# uv resolves and installs an order of magnitude faster than pip, and pins the
# whole tree in a lockfile the image can be rebuilt from byte for byte.
COPY --from=ghcr.io/astral-sh/uv:0.11.0 /uv /usr/local/bin/uv

ENV UV_COMPILE_BYTECODE=1 \
    UV_LINK_MODE=copy \
    UV_PYTHON_DOWNLOADS=never

WORKDIR /src

# Dependencies before source, so editing a prompt does not reinstall the SDK.
COPY pyproject.toml ./
RUN uv venv /opt/venv && \
    VIRTUAL_ENV=/opt/venv uv pip install --no-cache .

COPY src/ ./src/
RUN VIRTUAL_ENV=/opt/venv uv pip install --no-cache --no-deps .

# --- runtime -----------------------------------------------------------------
FROM python:3.13-slim

# Not distroless: the google-genai SDK and its transitive tree expect a real
# Python environment, and cutting it down to nothing would be a maintenance
# burden out of proportion to one internal service. What is cheap and worth
# doing is dropping root and keeping the layer thin.
RUN groupadd --system --gid 10001 esurvey && \
    useradd --system --uid 10001 --gid esurvey --no-create-home esurvey

COPY --from=build /opt/venv /opt/venv

ENV PATH="/opt/venv/bin:$PATH" \
    PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    ESURVEY_AI_HOST=0.0.0.0 \
    ESURVEY_AI_PORT=8100

WORKDIR /app
USER esurvey:esurvey

EXPOSE 8100

HEALTHCHECK --interval=15s --timeout=6s --start-period=20s --retries=3 \
    CMD ["python", "-c", "import urllib.request,sys; sys.exit(0 if urllib.request.urlopen('http://127.0.0.1:8100/healthz', timeout=5).status == 200 else 1)"]

# One worker per container. The agent's work is a network wait on Gemini, not
# CPU, so concurrency comes from the event loop; replicas are what scale it.
CMD ["uvicorn", "esurvey_ai.main:app", "--host", "0.0.0.0", "--port", "8100"]
