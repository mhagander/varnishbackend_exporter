# varnishbackend_exporter

## Summary

`varnishbackend_exporter` is an exporter for `prometheus` that counts
backend in different states in Varnish. It does so by connecting to
the Varnish management interface using TCP (typically on
localhost:6082) and collects the statistics at regular intervals
(they are not collected on scrape, but are cached locally in order
to respond quickly to the scrapes).

## Exported metrics

`varnishbackend_exporter` exports a single metric, with multiple labels.
The metric name is `varnish_backend_state`. In the simplest mode, only
one label is attached, `state`. This can be either `healthy` or `sick`,
and contains the count of backends in this state at the given moment.

When run in `director regexp mode`, it will also export a label named
`director`, which will be set to the name captured using the regexp
(see below).


### director regexp mode

The Varnish administration interface does not directly expose which
backends are used in which directors. But if consistent naming is used,
then `varnishbackend_exporter` can extract the director name (or any
other data of course, but it will be labeled as director) from the name
of the backend.

This extraction is done using a regular expression given to the
`-directorre` parameter. This regular expression must have a capture
group, and the value captured in this group will be the one labeled
as the director name.

For example, if a naming standard has backend names like
`servername_systemname`, we can extract systemname by passing
`-directorre ".*_([^_]+)$"`.

Any backend not being matched by the regexp will be labeled as `unknown`.


## Usage

  -directorre string
    	Regular expression extracting director name from backend name
  -varnish.interval int
    	Varnish checking interval (default 15)
  -varnish.port int
    	Port of Varnish to connect to (default 6082)
  -varnish.secret string
    	Filename of varnish secret file (default "/etc/varnish/secret")
  -version
    	Print version information.
  -web.listen-address string
    	Address to listen on for web interface and telemetry. (default ":9133")
  -web.telemetry-path string
    	Path under which to expose metrics. (default "/metrics")
