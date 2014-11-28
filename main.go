package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/kdar/factorlog"
)

var log = factorlog.New(os.Stdout, factorlog.NewStdFormatter("%{Date} %{Time} [%{sev}] :: %{Message}"))

type influxInsert []seriesData
type influxPoint []interface{}

type seriesData struct {
	Name    string        `json:"name"`
	Columns []string      `json:"columns"`
	Points  []influxPoint `json:"points"`
}

var timestamp = regexp.MustCompile("[(0-9)]+.[(0-9)]+")
var timeout = regexp.MustCompile("no answer")
var icmp = regexp.MustCompile("icmp_seq=([0-9]+)")
var timeExp = regexp.MustCompile("time=([0-9]+.[0-9]) ms")
var unreachable = regexp.MustCompile("Destination Host Unreachable")

type panther struct {
	lock sync.Mutex

	pendingMutex     sync.Mutex
	pendingSubmition []entry
}

type entry struct {
	Ts           time.Time
	Seq          uint16
	Timeout      bool
	Unreachable  bool
	ResponseTime float32
	TTL          int
	ResponseFrom string
}

func checkTimeout(c chan struct{}) {
	for {
		select {
			case <-c:
				return

			case <-time.After(30*time.Second):
				log.Warnf("timeout channel is still open!!")
		}
	}
}

func postJSONToInflux(jsonbytes []byte, url string) error {
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonbytes))
	if err != nil {
		return fmt.Errorf("NewRequest post to influxdb failed: %s", err.Error())
	}

	c := make(chan struct{})
	defer close(c)
	go checkTimeout(c)

	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Request to endpoint '%s' failed: %s", url, err.Error())
	}

	// fmt.Println("response Status:", resp.Status)
	// fmt.Println("response Headers:", resp.Header)
	// body, _ := ioutil.ReadAll(resp.Body)
	// fmt.Println("response Body:", string(body))

	defer resp.Body.Close()

	return nil
}

func dupl(input []entry) []entry {
	out := make([]entry, len(input))
	copy(out, input)
	return out
}

func (p *panther) flushEntries(url string, batchCount int) {
	p.pendingMutex.Lock()
	defer p.pendingMutex.Unlock()

	if len(p.pendingSubmition) < batchCount {
		return
	}

	insert := influxInsert{
		seriesData{
			Name:    "ping",
			Columns: []string{"time", "seq", "timeout", "unreachable", "responseTime", "ttl", "responseFrom"},
		},
	}

	for _, each := range p.pendingSubmition {
		point := influxPoint{}
		point = append(point, each.Ts.UnixNano()/1000/1000) // time in ms
		point = append(point, each.Seq)
		point = append(point, each.Timeout)
		point = append(point, each.Unreachable)
		point = append(point, each.ResponseTime)
		point = append(point, each.TTL)
		point = append(point, each.ResponseFrom)
		insert[0].Points = append(insert[0].Points, point)
	}

	b, err := json.Marshal(insert)
	if err != nil {
		log.Errorf("json.Marshal failed: %s", err.Error())
	}

	err = postJSONToInflux(b, url)
	if err != nil {
		log.Errorf("postJSONToInflux failed: %s", err.Error())
	}

	p.pendingSubmition = p.pendingSubmition[:0]
}

func (p *panther) appendEntry(e entry) {
	p.pendingMutex.Lock()
	defer p.pendingMutex.Unlock()

	p.pendingSubmition = append(p.pendingSubmition, e)
}

func (p *panther) submitEntry(e entry, url string, batchCount int) {

	go func() {
		p.appendEntry(e)
		p.flushEntries(url, batchCount)
	}()
}

func parseEntry(s string) (entry, error) {
	var e entry

	b := []byte(s)

	e.Timeout = timeout.Match(b)
	e.Unreachable = unreachable.Match(b)

	// Parse timestamp
	submatches := timestamp.FindSubmatch(b)
	if len(submatches) <= 0 {
		return e, fmt.Errorf("missing timestamp information")
	}

	ts, err := strconv.ParseFloat(string(submatches[0]), 64)
	if err != nil {
		return e, fmt.Errorf("invalid timestamp information (unparsable as float32)")
	}

	// must not continue as it will fail due to missing icmp
	if e.Unreachable {
		return e, nil
	}

	ns := ts * 1000.0 * 1000.0 * 1000.0
	//log.Infof("nanoseconds %f", ns)
	e.Ts = time.Unix(0, int64(ns))

	// TODO: parse IP?

	// parse icmp_seq=
	submatches = icmp.FindSubmatch(b)
	if len(submatches) < 2 {
		return e, fmt.Errorf("icmp_seq not found on string")
	}

	val, err := strconv.ParseUint(string(submatches[1]), 10, 16)
	if err != nil {
		return e, fmt.Errorf("invalid icmp_seq value")
	}
	e.Seq = uint16(val)

	// don't proceed for timeouts
	if e.Timeout {
		return e, nil
	}

	submatches = timeExp.FindSubmatch(b)
	if len(submatches) < 2 {
		return e, fmt.Errorf("invalid time= value")
	}

	rt, err := strconv.ParseFloat(string(submatches[1]), 32)
	if err != nil {
		return e, fmt.Errorf("parsing of response time failed for substring '%s'", string(submatches[1]))
	}

	e.ResponseTime = float32(rt)

	return e, nil
}

func main() {
	dbuser := flag.String("dbuser", "root", "Influx db username")
	dbpass := flag.String("dbpass", "root", "Influx db password")
	dbhost := flag.String("dbhost", "localhost", "host to use for submitting data to influxdb")
	dbport := flag.Int("dbport", 8086, "influx db port")
	dbname := flag.String("dbname", "pings", "Name of the influx database to submit data to")
	batch := flag.Int("batch", 1, "Batch entries together before sending to influx (useful for piping heaps of ping data)")
	flag.Parse()

	var endpoint = fmt.Sprintf("http://%s:%d/db/%s/series", *dbhost, *dbport, *dbname)
	var urlparams = fmt.Sprintf("?u=%s&p=%s", *dbuser, *dbpass)
	var url = endpoint + urlparams
	var batchCount = *batch
	if batchCount < 1 {
		batchCount = 1
	}

	log.Info("-> Expected input data has to be output from linux/unix: ping -O -W 1 <some ip>")
	log.Info("-> The output is expected to look like:")
	log.Info("->   '[1416956208.606609] 64 bytes from 8.8.8.8: icmp_seq=80 ttl=49 time=24.0 ms'")
	log.Info("-> or")
	log.Info("->   'no answer yet for icmp_seq=217'")

	pa := panther{}

	log.Info("-> Submitting pings to %s", endpoint)
	log.Info("-> Running pingpanther...")

	scanner := bufio.NewScanner(os.Stdin)

	for scanner.Scan() {
		e := entry{}

		line := scanner.Text()

		e, err := parseEntry(line)
		if err != nil {
			log.Errorf("%s <- Failed to parse because %s", line, err.Error())
			continue
		}

		pa.submitEntry(e, url, batchCount)

		log.Infof("%s <- ✓", line)
	}

	pa.flushEntries(url, 1)
}
