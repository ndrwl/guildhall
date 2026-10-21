//go:build linux

package artisan_test

import (
 "encoding/json"
 "os"
 "os/exec"
 "os/signal"
 "path/filepath"
 "strings"
 "syscall"
 "testing"
 "time"

 "github.com/ndrwl/guildhall/internal/artisan"
 "github.com/ndrwl/guildhall/internal/artisan/artisantest"
)

// TestLauncherLiveWorker is a subprocess fixture, not a replacement launcher.
func TestLauncherLiveWorker(t *testing.T) {
 dir := os.Getenv("GUILDHALL_LIVE_DIR")
 if dir == "" { t.Skip("subprocess only") }
 if os.Getenv("GUILDHALL_LIVE_CHILD") == "1" {
  signals := make(chan os.Signal, 1)
  signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
  if err := os.WriteFile(filepath.Join(dir,"child-ready"), []byte("ready"),0600); err != nil { os.Exit(2) }
  for {
   select {
   case s := <-signals:
    _ = os.WriteFile(filepath.Join(dir,"child-signal"), []byte(s.String()),0600)
    os.Exit(0)
   default:
   }
   if _, err := os.Stat(filepath.Join(dir,"release")); err == nil { os.Exit(0) }
   time.Sleep(10*time.Millisecond)
  }
 }
 child := exec.Command(os.Args[0], "-test.run=^TestLauncherLiveWorker$")
 child.Env = append(os.Environ(), "GUILDHALL_LIVE_CHILD=1")
 if err := child.Start(); err != nil { os.Exit(2) }
 for {
  if _, err := os.Stat(filepath.Join(dir,"cancelled")); err == nil { os.Exit(0) }
  time.Sleep(10*time.Millisecond)
 }
}

// TestLauncherLiveCancelledTree exercises the public Go API with native Linux
// containment and an actual orphaned child. No mocked Unit controls its exit.
func TestLauncherLiveCancelledTree(t *testing.T) {
 for _, mode := range []string{"cooperative-release", "explicit-stop"} {
  t.Run(mode, func(t *testing.T) {
   dir := t.TempDir()
   // Force the process-group backend: tests must not create host cgroups.
   u, err := artisan.NewSubreaperUnit("live cancellation test")
   if err != nil { t.Fatal(err) }
   defer u.Close()
   files := artisantest.NewFiles(dir)
   l, err := artisan.NewLauncher(artisan.LauncherConfig{Dir:dir, Unit:u, Files:files,
    Ladder:artisan.Ladder{Cooperative:100*time.Millisecond, Interrupt:time.Second, Terminate:time.Second, Kill:time.Second}})
   if err != nil { t.Fatal(err) }
   cmd := exec.Command(os.Args[0], "-test.run=^TestLauncherLiveWorker$")
   cmd.Env = append(os.Environ(), "GUILDHALL_LIVE_DIR="+dir)
   done := make(chan struct{})
   var out artisan.Outcome
   var runErr error
   go func(){ defer close(done); out,runErr=l.Run(cmd,nil) }()
   wait := func(what string, f func() bool) {
    t.Helper()
    deadline := time.Now().Add(5*time.Second)
    for !f() { if time.Now().After(deadline) { t.Fatalf("timeout: %s",what) }; time.Sleep(10*time.Millisecond) }
   }
   wait("child ready",func()bool{ _,err:=os.Stat(filepath.Join(dir,"child-ready"));return err==nil })
   owned,err:=u.Members(); if err!=nil || len(owned)!=2 { t.Fatalf("members=%v: %v",owned,err) }
   if _,err=l.Cancel();err!=nil {t.Fatal(err)}
   wait("parent exit",func()bool{ select {case <-u.Exited():return true;default:return false} })
   wait("cooperative timeout",func()bool{ b,_:=os.ReadFile(filepath.Join(dir,artisan.EventsFile));return strings.Contains(string(b),"rung-timeout") })
   if _,err=os.Stat(filepath.Join(dir,"child-signal")); !os.IsNotExist(err) {t.Fatalf("child signalled after marker-only Cancel: %v",err)}
   select {case <-done:t.Fatal("launcher ended while child remains");default:}
   members,err:=u.Members();if err!=nil || len(members)!=1 {t.Fatalf("surviving child=%v: %v",members,err)}
   t.Logf("Cancel past cooperative bound: parent exited, child remains unsignalled; containment=%+v",u.Containment())
   if mode=="explicit-stop" {
    stopped:=make(chan error,1);go func(){_,err:=l.Stop();stopped<-err}()
    select {case err:=<-stopped:if err!=nil {t.Fatal(err)};case <-time.After(5*time.Second):t.Fatal("Stop timeout")}
   } else if err=os.WriteFile(filepath.Join(dir,"release"),[]byte("release"),0600);err!=nil {t.Fatal(err)}
   select {case <-done:case <-time.After(5*time.Second):t.Fatal("Run timeout")}
   wantRung:=1;if mode=="explicit-stop" {wantRung=2}
   if runErr!=nil || out.State!=artisan.Cancelled || out.Rung!=wantRung {t.Fatalf("outcome=%+v error=%v",out,runErr)}
   b,err:=os.ReadFile(filepath.Join(dir,artisan.OutcomeFile));if err!=nil {t.Fatal(err)}
   var persisted artisan.Outcome;if err=json.Unmarshal(b,&persisted);err!=nil || persisted.State!=out.State || persisted.Rung!=out.Rung {t.Fatalf("persisted=%s: %v",b,err)}
   t.Logf("outcome.json: %s",b)
   events,err:=os.ReadFile(filepath.Join(dir,artisan.EventsFile));if err!=nil {t.Fatal(err)}
   t.Logf("events.jsonl:\n%s",events)
   if mode=="explicit-stop" {b,err:=os.ReadFile(filepath.Join(dir,"child-signal"));if err!=nil || string(b)!="interrupt" {t.Fatalf("signal=%q: %v",b,err)}}
   members,err=u.Members();if err!=nil || len(members)!=0 {t.Fatalf("remaining=%v: %v",members,err)}
  })
 }
}
