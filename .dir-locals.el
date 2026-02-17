((go-ts-mode
  .
  ((dape-configs
    .
    ((go-debug
      modes (go-mode go-ts-mode)
      command "dlv"
      ;;command-args ("dap" "--listen" "127.0.0.1::autoport" "--log" "--log-output" "debugger,gdbwire,lldbout,debuglineerr,rpc,dap,fncall,minidump,stack")
      command-args ("dap" "--listen" "127.0.0.1::autoport")
      command-cwd dape-command-cwd
      command-insert-stderr t
      ensure dape-ensure-command
      port :autoport
      :args ["golang.org/x/tools@latest"]
      :buildFlags ["-race" "-buildvcs=true"]
      :request "launch"
      :type "go"
      :cwd "."
      :program "./cmd/gomoddepgraph"))))))
