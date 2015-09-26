gfsocket
========

GO client for Freeswitch-Inbound.
 - With easy filter system for reacting to events.

Cliente GO para Freeswitch Event Socket Inbound.
 - Con un sistema de filtra para reacionar a los eventos.

How To Play
~~~~~~~~~~~

See *gfsocket_test.go*

Ver *gfsocket_test.go*

~~~go
fs, _ := Dial(FREESWITCH_ADDR, FREESWITCH_PASSWORD)

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
fs.Api("originate user/bad &hangup()")
fs.BGApi("originate user/bad &hangup()", nil)


fs.Cmd("exit")

~~~