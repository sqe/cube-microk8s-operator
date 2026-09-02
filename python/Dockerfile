FROM python:3.13-slim@sha256:9d2e5553305c7c7b0097999bb17187c69b921ccd6bc9d40e4bb5ebe652c00285

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1
WORKDIR /app
COPY python/requirements.txt ./requirements.txt
RUN pip install --no-cache-dir --disable-pip-version-check -r requirements.txt
COPY python/cube_kopf/ ./cube_kopf/
USER 65532:65532
ENTRYPOINT ["kopf", "run", "--standalone", "--all-namespaces", "--liveness=http://0.0.0.0:8081/healthz", "-m", "cube_kopf.controller"]
