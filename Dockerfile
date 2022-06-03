ARG ARCH=
FROM golang:1.18-bullseye as builder

# setup bladerf commands
RUN mkdir -p /src
WORKDIR /src

RUN apt-get update -q && apt-get install -yq libusb-1.0-0-dev libusb-1.0-0 build-essential cmake libncurses5-dev libtecla1 libtecla-dev pkg-config git wget

# build bladeRF tools. Install to /usr and to /bladerf-lib so we can easily access the
# libs in the correct place and they are in a convenient place to copy to the final image
RUN git clone https://github.com/Nuand/bladeRF.git && \
    cd bladeRF && \
    git checkout 2021.03 && \
    mkdir build && \
    cd build && \
    cmake -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX=/usr ../ && \
    cmake -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX=/bladerf-lib ../ && \
    make -j3 install

# Grab all the FPGA images
#RUN mkdir /bladeRF-images && cd /bladeRF-images && \
#    wget https://www.nuand.com/fpga/hostedxA4-latest.rbf && \
#    wget https://www.nuand.com/fpga/hostedxA9-latest.rbf && \
#    wget https://www.nuand.com/fpga/hostedx40-latest.rbf && \
#    wget https://www.nuand.com/fpga/hostedx115-latest.rbf
#
# build device plugin
ENV GOOS=linux
ENV GOARCH=arm
ENV GOPATH=/usr/src

ADD . /usr/src/bladerf-device-plugin
WORKDIR /usr/src/bladerf-device-plugin
RUN go mod tidy && go build -o bladerf-device-plugin

FROM alpine:latest

COPY --from=builder /bladerf-lib/lib/* /usr/lib/
COPY --from=builder /bladerf-lib/bin/bladeRF-cli /usr/bin/
COPY --from=builder /etc/udev/rules.d/*nuand* /etc/udev/rules.d/
COPY --from=builder /usr/src/bladerf-device-plugin/bladerf-device-plugin /
ENTRYPOINT ["/bladerf-device-plugin", "-logtostderr", "true"]
