ARG DOCKTINEL_IMAGE=docktinel:local
FROM ${DOCKTINEL_IMAGE} AS sentinel
FROM python:3.13-alpine
COPY --from=sentinel /usr/local/bin/docktinel /usr/local/bin/docktinel
WORKDIR /srv
COPY entrypoint.sh check.py /opt/demo/
RUN chmod +x /opt/demo/entrypoint.sh
ENV TINI_KILL_PROCESS_GROUP=1
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=5s --start-period=30s --retries=3 \
    CMD ["docktinel", "healthcheck", "--startup-grace", "30s"]
ENTRYPOINT ["/opt/demo/entrypoint.sh"]
