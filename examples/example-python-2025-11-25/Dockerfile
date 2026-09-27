FROM python:3.12-alpine

WORKDIR /app
COPY app.py /app/app.py
RUN pip install --no-cache-dir "mcp[cli]<2"

RUN adduser -D -H -u 10001 app
USER 10001

EXPOSE 8088

ENTRYPOINT ["python", "/app/app.py"]
