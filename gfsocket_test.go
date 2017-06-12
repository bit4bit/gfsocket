package gfsocket

import (
	"fmt"
	"testing"
	"time"
)

//Cambiar para que las pruebas sean efectivas
const FREESWITCH_ADDR = "172.18.0.22:8021"
const FREESWITCH_PASSWORD = "ClueCon"

func TestValidAuthentication(t *testing.T) {
	_, err := Dial(FREESWITCH_ADDR, FREESWITCH_PASSWORD)
	if err != nil {
		t.Errorf(err.Error())
		return
	}
}

func TestInvalidAuthentication(t *testing.T) {
	_, err := Dial(FREESWITCH_ADDR, "ClueoCon")
	if err == nil {
		t.Errorf("Expected not authentaction")
		return
	}
}

func TestApi(t *testing.T) {
	fs, err := Dial(FREESWITCH_ADDR, FREESWITCH_PASSWORD)
	if err != nil {
		t.Errorf("Expected  authentaction")
		return
	}

	var apiRes ApiResponse

	apiRes = fs.Api("originate user/notexist &hangup()")
	if apiRes.Content != "SUBSCRIBER_ABSENT" {
		t.Errorf("Failed originate not user")
	}
	fs.Close()
}

func TestCmd(t *testing.T) {
	fs, err := Dial(FREESWITCH_ADDR, FREESWITCH_PASSWORD)
	if err != nil {
		t.Errorf("Expected authentaction")
		return
	}

	var cmdReply CommandReply
	cmdReply = fs.Cmd("exit")
	if cmdReply.Status != "+OK" && cmdReply.Content != "bye" {
		t.Errorf("Failed, command exit")
	}
	fs.Close()
}

func ExampleM_HandleFunc() {
	fs, _ := Dial(FREESWITCH_ADDR, FREESWITCH_PASSWORD)

	var output chan string = make(chan string, 1)
	fs.HandleChanFunc(Filter{"Content-Type": "text/disconnect-notice"}, func(fs *Connection, ch chan interface{}) {
		for {
			recv := <-ch
			recv = recv
			output <- "HANDLER_CHAN_DISCONNECT"
		}
	})

	//wait specific event
	fs.HandleChanFunc(Filter{"Event-Name": "BACKGROUND_JOB"}, func(fs *Connection, ch chan interface{}) {
		for {
			recv := <-ch
			//recv = recv
			output <- "HANDLER_BACKGROUND_JOB:" + recv.(Event).Content.Get("Job-Command")
		}
	})

	fs.HandleFunc(Filter{"Event-Name": "API"}, func(ev interface{}) {
		output <- "HANDLER_EVENT_API:" + ev.(Event).Content.Get("Api-Command")
	})

	fs.Cmd("event plain all")
	fs.Api("show help")
	time.Sleep(time.Second)
	fmt.Println(<-output)

	fs.Api("originate user/bad &hangup()")
	time.Sleep(time.Second)
	fmt.Println(<-output)

	fs.BGApi("originate user/bad &hangup()", nil)
	time.Sleep(2 * time.Second)
	fmt.Println(<-output) //event api
	fmt.Println(<-output) //background job

	fs.Cmd("exit")
	time.Sleep(time.Second)
	fs.Close()
	// Output:
	// HANDLER_EVENT_API:show
	// HANDLER_EVENT_API:originate
	// HANDLER_BACKGROUND_JOB:originate
	// HANDLER_EVENT_API:originate

}
