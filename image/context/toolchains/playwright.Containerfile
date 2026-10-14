# Browser binaries live outside the home volume and are available to every agent.
ENV PLAYWRIGHT_BROWSERS_PATH=/opt/playwright-browsers
ENV NODE_PATH=/usr/local/lib/node_modules
RUN npm install --global --prefix /usr/local playwright @playwright/test \
    && playwright install --with-deps chromium firefox webkit \
    && chmod -R a+rX /opt/playwright-browsers \
    && rm -rf /var/lib/apt/lists/* /root/.npm
