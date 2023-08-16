// Copyright 2021 Chaos Mesh Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//

package util

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"

	"github.com/chaos-mesh/chaos-mesh/pkg/bpm"
	"github.com/chaos-mesh/chaos-mesh/pkg/chaosdaemon/graph"
)

var (
	defaultHttpClient = http.Client{
		Transport: &http.Transport{},
		Timeout:   5 * time.Second,
	}
)

const (
	RuntimeKey  = "runtime"
	HOST_IP_ENV = "PA_HOST_IP"
)

type SaturnApiResult struct {
	Metric map[string]interface{}
	Value  []interface{}
}

type SaturnApiData struct {
	ResultType string
	Result     []SaturnApiResult
}
type SaturnApiResp struct {
	Status string
	Data   SaturnApiData
}

func ParseRuntime(saturnApi string, auth string) (runtime string, err error) {
	var (
		request *http.Request
		resp    *http.Response
	)

	hostIp := os.Getenv(HOST_IP_ENV)
	if len(hostIp) == 0 {
		return runtime, fmt.Errorf("not found hostIP from env")
	}

	queryURL := saturnApi + fmt.Sprintf("/api/v1/query?query=pingan_k8s_os_check_runtime_service{host_ip='%s'}", hostIp)

	if request, err = http.NewRequest(http.MethodGet, queryURL, nil); err != nil {
		return
	}
	request.Header.Set("Authorization", auth)

	if resp, err = defaultHttpClient.Do(request); err != nil {
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		err = fmt.Errorf("response status code is invalid: %d", resp.StatusCode)
		return
	}

	var saturnApiResp SaturnApiResp
	if err = json.NewDecoder(resp.Body).Decode(&saturnApiResp); err != nil {
		return
	}
	if len(saturnApiResp.Data.Result) < 1 {
		err = fmt.Errorf("not found result from saturn api by nodeIP %s", "")
		return
	}

	if runtimeInterface, ok := saturnApiResp.Data.Result[0].Metric[RuntimeKey]; !ok {
		err = fmt.Errorf("not found runtime in Metric result")
		return
	} else {
		if runtime, ok = runtimeInterface.(string); !ok {
			err = fmt.Errorf("runtime type is invalid")
			return
		}
	}
	return
}

// ReadCommName returns the command name of process
func ReadCommName(pid int) (string, error) {
	f, err := os.Open(fmt.Sprintf("%s/%d/comm", bpm.DefaultProcPrefix, pid))
	if err != nil {
		return "", err
	}
	defer f.Close()

	b, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}

	return string(b), nil
}

// GetChildProcesses will return all child processes's pid. Include all generations.
// only return error when /proc/pid/tasks cannot be read
func GetChildProcesses(ppid uint32, logger logr.Logger) ([]uint32, error) {
	procs, err := os.ReadDir(bpm.DefaultProcPrefix)
	if err != nil {
		return nil, errors.Wrapf(err, "read /proc/pid/tasks , ppid : %d", ppid)
	}

	type processPair struct {
		Pid  uint32
		Ppid uint32
	}

	pairs := make(chan processPair)
	done := make(chan bool)

	go func() {
		var wg sync.WaitGroup

		for _, proc := range procs {
			_, err := strconv.ParseUint(proc.Name(), 10, 32)
			if err != nil {
				continue
			}

			statusPath := bpm.DefaultProcPrefix + "/" + proc.Name() + "/stat"

			wg.Add(1)
			go func() {
				defer wg.Done()

				reader, err := os.Open(statusPath)
				if err != nil {
					logger.Error(err, "read status file error", "path", statusPath)
					return
				}
				defer reader.Close()

				var (
					pid    uint32
					comm   string
					state  string
					parent uint32
				)
				// according to procfs's man page
				fmt.Fscanf(reader, "%d %s %s %d", &pid, &comm, &state, &parent)

				pairs <- processPair{
					Pid:  pid,
					Ppid: parent,
				}
			}()
		}

		wg.Wait()
		done <- true
	}()

	processGraph := graph.NewGraph()
	for {
		select {
		case pair := <-pairs:
			processGraph.Insert(pair.Ppid, pair.Pid)
		case <-done:
			return processGraph.Flatten(ppid, logger), nil
		}
	}
}

func EncodeOutputToError(output []byte, err error) error {
	return errors.Errorf("error code: %v, msg: %s", err, string(output))
}
