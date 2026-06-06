FROM chromedp/headless-shell:stable@sha256:8b36bc4bca3f394103db8a2e60f0053969a277b3918abc39acfee819168c4f79

LABEL org.opencontainers.image.source="https://github.com/klarlabs-studio/scout"
LABEL org.opencontainers.image.description="AI-powered browser automation MCP server"
LABEL org.opencontainers.image.licenses="MIT"
LABEL io.modelcontextprotocol.server.name="io.github.klarlabs-studio/scout"

COPY scout /usr/local/bin/scout

ENTRYPOINT ["scout"]
CMD ["mcp", "serve"]
