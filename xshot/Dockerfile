FROM node:22-slim

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
    chromium \
    fonts-liberation \
    fonts-noto-color-emoji \
    && rm -rf /var/lib/apt/lists/*

ENV PUPPETEER_SKIP_DOWNLOAD=true
ENV PUPPETEER_EXECUTABLE_PATH=/usr/bin/chromium

RUN useradd -m -s /bin/bash xshot
WORKDIR /app

COPY --chown=xshot:xshot package.json package-lock.json ./
RUN npm ci --omit=dev && chown -R xshot:xshot /app

COPY --chown=xshot:xshot server.js ./

USER xshot
EXPOSE 3210

CMD ["node", "server.js"]
