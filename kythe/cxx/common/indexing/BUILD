package(default_visibility = ["//kythe:default_visibility"])

cc_library(
    name = "lib",
    srcs = [
        "KytheClaimClient.cc",
        "KytheGraphRecorder.cc",
        "KytheOutputStream.cc",
        "KytheVFS.cc",
    ],
    hdrs = [
        "KytheClaimClient.h",
        "KytheGraphRecorder.h",
        "KytheOutputStream.h",
        "KytheVFS.h",
        "MaybeFew.h",
    ],
    copts = [
        "-Wno-non-virtual-dtor",
        "-Wno-unused-variable",
        "-Wno-implicit-fallthrough",
    ],
    deps = [
        "//external:libmemcached",
        "//kythe/cxx/common:index_pack",
        "//kythe/cxx/common:json_proto",
        "//kythe/cxx/common:lib",
        "//kythe/proto:analysis_proto_cc",
        "//kythe/proto:storage_proto_cc",
        "//third_party/proto:protobuf",
        "@com_github_gflags_gflags//:gflags",
    ],
)

cc_library(
    name = "testlib",
    hdrs = [
        "RecordingOutputStream.h",
    ],
    deps = [
        ":lib",
    ],
)

cc_library(
    name = "frontend",
    srcs = [
        "frontend.cc",
    ],
    hdrs = [
        "frontend.h",
    ],
    copts = [
        "-Wno-non-virtual-dtor",
        "-Wno-unused-variable",
        "-Wno-implicit-fallthrough",
    ],
    deps = [
        ":lib",
        "//kythe/proto:claim_proto_cc",
    ],
)
