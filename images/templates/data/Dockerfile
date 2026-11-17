# SPDX-License-Identifier: GPL-2.0-or-later
# Example environment: the sandbox image plus a Python data-science stack (NumPy, pandas, SciPy, scikit-learn, Matplotlib, Jupyter). Build it on the Docker host, then
# approve it in the admin app (Environments):
#   docker build -t syntaxis-env-data:1 --build-arg BASE=syntaxis-sandbox:latest images/templates/data
ARG BASE=syntaxis-sandbox:latest
FROM ${BASE}
USER 0
RUN pip3 install --break-system-packages --no-cache-dir numpy pandas scipy scikit-learn matplotlib jupyterlab \
    && rm -rf /root/.cache
ENV MPLCONFIGDIR=/tmp/matplotlib JUPYTER_CONFIG_DIR=/tmp/jupyter JUPYTER_DATA_DIR=/tmp/jupyter-data
USER 10001:10001
