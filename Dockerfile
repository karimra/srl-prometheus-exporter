ARG SR_LINUX_RELEASE
FROM srl/custombase:$SR_LINUX_RELEASE AS target-image

COPY srl-prometheus-exporter-1.0.0.x86_64.rpm /tmp/

# Create a Python virtual environment, note --upgrade is broken. Tried without --system-site-packages --without-pip
RUN sudo yum localinstall /tmp/srl-prometheus-exporter-1.0.0.x86_64.rpm -y

# Using a build arg to set the release tag, set a default for running docker build manually
ARG SRL_PROMETHEUS_EXPORTER_RELEASE="[custom build]"
ENV SRL_PROMETHEUS_EXPORTER_RELEASE=$SRL_PROMETHEUS_EXPORTER_RELEASE

