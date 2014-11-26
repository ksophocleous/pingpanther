# pingpanther

Parses specific ping output (e.g. `ping -O -D -W 1`) and submits the response time and timeouts (or unreachability) to an influxdb server.

## Reason

I made this to maintain a graph of the stability of my internet connection, which at the moment it is absolutely terrible.

The application should continue working even when internet goes down.

## Install

Provided your GOPATH is set, this step is easy

`go get github.com/ksophocleous/pingpanther`

## Example usage

`ping -O -D -W 1 8.8.8.8 | pingpanther -dbhost=localhost -dbname=pings -dbuser=root -dbpass=root`

Shown here are the default flags of pingpanther
