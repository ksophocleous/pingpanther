# pingpanther

Parses specific ping output (e.g. `ping -O -D -W 1`) and submits the response time and timeouts (or unreachability) to an influxdb server.

## Reason

I made this to maintain a graph of the stability of my internet connection, which at the moment it is absolutely terrible.
