# Box (Node.js SMTP relay skeleton) — NO DB, NO KMS. Pure mTLS client.
FROM node:20-alpine
RUN apk add --no-cache curl && adduser -D -u 10002 box
WORKDIR /app
# package.json has NO dependencies (structural no-DB/no-KMS boundary); copy source only.
COPY services/box/package.json ./
COPY services/box/src ./src
USER box
ENTRYPOINT ["node", "src/index.js"]
