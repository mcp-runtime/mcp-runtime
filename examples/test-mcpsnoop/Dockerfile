FROM python:3.12-slim

WORKDIR /app
COPY requirements.txt /app/requirements.txt
RUN pip install --no-cache-dir -r requirements.txt
COPY app.py /app/app.py

USER 10001:10001
EXPOSE 8088
ENTRYPOINT ["python", "/app/app.py"]
