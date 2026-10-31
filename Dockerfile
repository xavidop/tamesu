# ghcr.io/xavidop/tamesu: the tamesu CLI with Chromium preinstalled, for CI.
# GoReleaser copies the linux binary it built into the build context.
FROM debian:bookworm-slim
RUN apt-get update \
	&& apt-get install -y --no-install-recommends ca-certificates chromium fonts-liberation tzdata \
	&& rm -rf /var/lib/apt/lists/*
ENV TAMESU_CHROMIUM=/usr/bin/chromium
COPY tamesu /usr/local/bin/tamesu
WORKDIR /work
ENTRYPOINT ["tamesu"]
CMD ["--help"]
