FROM node:18-alpine

# Install Docker CLI so the script can run docker ps / docker logs
RUN apk add --no-cache docker-cli

WORKDIR /app

COPY package*.json ./
RUN npm install --production

COPY index.js ./

ENV PORT=9090
ENV LABEL_FILTER=publishLog=true
ENV RESCAN_MS=5000

EXPOSE 9090

CMD ["node", "index.js"]
