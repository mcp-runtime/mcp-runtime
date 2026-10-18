FROM python:3.12-alpine
WORKDIR /app
COPY server.py /app/server.py
RUN adduser -D -H -u 10001 app
USER 10001
EXPOSE 8088
ENTRYPOINT ["python", "/app/server.py"]
