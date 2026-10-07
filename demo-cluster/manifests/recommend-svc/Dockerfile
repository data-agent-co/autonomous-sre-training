FROM python:3.14.8-alpine3.24
RUN adduser -D -u 10001 app
WORKDIR /app
RUN pip install --no-cache-dir prometheus_client==0.26.0
COPY main.py leak.py ./
USER 10001
EXPOSE 8080
# No ENTRYPOINT — K8s Deployment specifies command/args per-container.
# main.py runs the HTTP service; leak.py runs the fault fixture.
CMD ["python", "main.py"]
