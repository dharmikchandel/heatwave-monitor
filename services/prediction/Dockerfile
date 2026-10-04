# syntax=docker/dockerfile:1
#
# The prediction service (Python). Only runtime dependencies are installed; the
# training tooling lives in scripts/train and is never shipped.

FROM python:3.13-slim AS build
RUN pip install --no-cache-dir uv==0.12.20
WORKDIR /app
ENV UV_COMPILE_BYTECODE=1 UV_LINK_MODE=copy UV_PYTHON_DOWNLOADS=0
COPY pyproject.toml uv.lock ./
# Exactly the versions in uv.lock, without dev dependencies (pytest, httpx).
RUN --mount=type=cache,target=/root/.cache/uv uv sync --frozen --no-dev --no-install-project

FROM python:3.13-slim
ARG REVISION=unknown
LABEL org.opencontainers.image.title="heatwave-monitor prediction" \
      org.opencontainers.image.source="https://github.com/dharmikchandel/heatwave-monitor" \
      org.opencontainers.image.revision="${REVISION}"

RUN groupadd -r -g 10001 app && useradd -r -u 10001 -g app -d /nonexistent -s /usr/sbin/nologin app \
 && mkdir /data && chown app:app /data
WORKDIR /app
COPY --from=build /app/.venv /app/.venv
COPY src ./src

ENV PATH=/app/.venv/bin:$PATH \
    PYTHONPATH=/app/src \
    PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PORT=8083 \
    DB_PATH=/data/prediction.db

USER 10001:10001
VOLUME ["/data"]
HEALTHCHECK --interval=10s --timeout=4s --start-period=15s --retries=3 \
    CMD python -c "import os, urllib.request; urllib.request.urlopen('http://127.0.0.1:%s/readyz' % os.environ['PORT'], timeout=3)"
CMD ["python", "-m", "prediction"]
