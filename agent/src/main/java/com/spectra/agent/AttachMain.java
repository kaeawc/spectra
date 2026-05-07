package com.spectra.agent;

import com.sun.tools.attach.VirtualMachine;

public final class AttachMain {
    private AttachMain() {
    }

    public static void main(String[] args) throws Exception {
        if (args.length < 2) {
            System.err.println("usage: AttachMain <pid> <agent.jar> [options]");
            System.exit(2);
        }
        VirtualMachine vm = VirtualMachine.attach(args[0]);
        try {
            String options = args.length > 2 ? args[2] : "";
            vm.loadAgent(args[1], options);
        } finally {
            vm.detach();
        }
    }
}
