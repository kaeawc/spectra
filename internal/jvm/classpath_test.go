package jvm

import (
	"reflect"
	"testing"
)

func TestParseLaunchArgs(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want LaunchClassPath
	}{
		{"cp", []string{"/usr/bin/java", "-Xmx1g", "-cp", "/app/a.jar:lib/*:classes", "com.example.Main", "-cp", "ignored"},
			LaunchClassPath{ClassPath: []string{"/app/a.jar", "lib/*", "classes"}, Explicit: true}},
		{"classpath long form", []string{"java", "--class-path=/x.jar", "Main"},
			LaunchClassPath{ClassPath: []string{"/x.jar"}, Explicit: true}},
		{"module path value skipped", []string{"java", "--add-modules", "ALL-SYSTEM", "-classpath", "/y", "Main"},
			LaunchClassPath{ClassPath: []string{"/y"}, Explicit: true}},
		{"jar overrides cp", []string{"java", "-cp", "/ignored", "-jar", "app.jar", "--port", "8080"},
			LaunchClassPath{Jar: "app.jar", Explicit: true}},
		{"default dot", []string{"/opt/jdk/bin/java", "-Dx=y", "Main"}, LaunchClassPath{}},
		{"argfile", []string{"java", "@/tmp/args", "Main"}, LaunchClassPath{Unknown: true}},
		{"jar without value", []string{"java", "-jar"}, LaunchClassPath{Unknown: true}},
		{"native launcher", []string{"/Applications/IntelliJ IDEA.app/Contents/MacOS/idea"}, LaunchClassPath{Unknown: true}},
		{"native launcher with cp", []string{"gradle-daemon", "-cp", "/g.jar", "Main"},
			LaunchClassPath{ClassPath: []string{"/g.jar"}, Explicit: true}},
		{"empty", nil, LaunchClassPath{Unknown: true}},
	}
	for _, c := range cases {
		if got := ParseLaunchArgs(c.argv); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestNeedsCwd(t *testing.T) {
	cases := map[bool][]LaunchClassPath{
		true:  {{Jar: "app.jar"}, {}, {Explicit: true, ClassPath: []string{"/a.jar", "lib/*"}}},
		false: {{Jar: "/app.jar"}, {Explicit: true, ClassPath: []string{"/a.jar"}}},
	}
	for want, cps := range cases {
		for _, cp := range cps {
			if got := cp.NeedsCwd(); got != want {
				t.Errorf("%+v.NeedsCwd() = %v", cp, got)
			}
		}
	}
}

func TestFrameClass(t *testing.T) {
	cases := map[string]string{
		"at com.example.A.run(A.java:10)":                                    "com.example.A",
		"at java.base@21.0.1/java.lang.Thread.run(Thread.java:1583)":         "java.lang.Thread",
		"at app//com.example.Outer$Inner.lambda$go$0(Outer.java:5)":          "com.example.Outer$Inner",
		"at com.example.A.<init>(A.java:3)":                                  "com.example.A",
		"at Main.main(Main.java:1)":                                          "Main",
		"at java.base/jdk.internal.misc.Unsafe.park(Native Method)":          "jdk.internal.misc.Unsafe",
		"at com.example.A$$Lambda/0x000000080100a000.run(Unknown Source)":    "",
		"at com.example.A$$Lambda$14/0x0000000800c03000.accept(Unknown Src)": "",
		"- locked <0x1> (a java.lang.Object)":                                "",
		"java.lang.Thread.State: RUNNABLE":                                   "",
	}
	for in, want := range cases {
		got, ok := FrameClass(in)
		if got != want || ok != (want != "") {
			t.Errorf("FrameClass(%q) = %q %v, want %q", in, got, ok, want)
		}
	}
}
