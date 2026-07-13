ARG AGENTNET_UV_IMAGE_DIGEST
ARG AGENTNET_PYTHON_BASE_DIGEST
FROM ghcr.io/astral-sh/uv@sha256:${AGENTNET_UV_IMAGE_DIGEST} AS uv-bin
FROM python:3.13.13-slim-bookworm@sha256:${AGENTNET_PYTHON_BASE_DIGEST} AS runtime

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    UV_COMPILE_BYTECODE=1 \
    UV_LINK_MODE=copy \
    PATH="/opt/agentnet/.venv/bin:${PATH}"

WORKDIR /opt/agentnet

COPY --from=uv-bin /uv /usr/local/bin/uv
COPY pyproject.toml uv.lock README.md RELEASE_MANIFEST.json REQUIREMENTS_STATUS.md ./
COPY schemas ./schemas
RUN uv sync --frozen --no-default-groups --group build --no-install-project

COPY src ./src

RUN uv sync --frozen --no-default-groups --group build --no-editable --no-build-isolation \
    && groupadd --system --gid 10001 agentnet \
    && useradd --system --uid 10001 --gid agentnet --home-dir /nonexistent --shell /usr/sbin/nologin agentnet \
    && install -d -m 0700 -o agentnet -g agentnet /var/lib/agentnet/secrets /var/lib/agentnet/artifacts

COPY --chown=agentnet:agentnet deploy/render_and_run.py /opt/agentnet/deploy/render_and_run.py

USER 10001:10001
STOPSIGNAL SIGTERM
ENTRYPOINT ["python", "/opt/agentnet/deploy/render_and_run.py"]
