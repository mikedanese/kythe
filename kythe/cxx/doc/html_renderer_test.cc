/*
 * Copyright 2016 Google Inc. All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *   http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

#include "kythe/cxx/doc/html_renderer.h"
#include "glog/logging.h"
#include "google/protobuf/text_format.h"
#include "gtest/gtest.h"
#include "kythe/cxx/doc/html_markup_handler.h"
#include "kythe/cxx/doc/javadoxygen_markup_handler.h"

namespace kythe {
namespace {
class HtmlRendererTest : public ::testing::Test {
 public:
  HtmlRendererTest() {
    options_.make_link_uri = [](const proto::Anchor &anchor) {
      return anchor.parent();
    };
    options_.node_info = [this](const std::string &ticket) {
      auto record = node_info_.find(ticket);
      return record != node_info_.end() ? &record->second : nullptr;
    };
    options_.anchor_for_ticket = [this](const std::string &ticket) {
      auto record = definition_locations_.find(ticket);
      return record != definition_locations_.end() ? &record->second : nullptr;
    };
    node_info_["kythe://foo"].set_definition("kythe://food");
    node_info_["kythe://bar"].set_definition("kythe://bard");
    definition_locations_["kythe://food"].set_parent("kythe://foop");
    definition_locations_["kythe://bard"].set_parent("kythe://barp&q=1<");
  }

 protected:
  std::string RenderAsciiProtoDocument(const char *document_pb) {
    proto::DocumentationReply::Document document;
    if (!google::protobuf::TextFormat::ParseFromString(document_pb,
                                                       &document)) {
      return "(invalid ascii protobuf)";
    }
    Printable printable(document.text());
    return kythe::RenderHtml(options_, printable);
  }
  std::string RenderJavadoc(const char *raw_text) {
    proto::Printable reply_;
    reply_.set_raw_text(raw_text);
    Printable input(reply_);
    auto output = HandleMarkup({ParseJavadoxygen}, input);
    return kythe::RenderHtml(options_, output);
  }
  std::string RenderHtml(const char *raw_text) {
    proto::Printable reply_;
    reply_.set_raw_text(raw_text);
    Printable input(reply_);
    auto output = HandleMarkup({ParseHtml}, input);
    return kythe::RenderHtml(options_, output);
  }
  kythe::HtmlRendererOptions options_;
  std::map<std::string, proto::NodeInfo> node_info_;
  std::map<std::string, proto::Anchor> definition_locations_;
};
TEST_F(HtmlRendererTest, RenderEmptyDoc) {
  EXPECT_EQ("", RenderAsciiProtoDocument(""));
}
TEST_F(HtmlRendererTest, RenderSimpleDoc) {
  EXPECT_EQ("Hello, world!", RenderAsciiProtoDocument(R"(
      text { raw_text: "Hello, world!" }
  )"));
}
TEST_F(HtmlRendererTest, RenderLink) {
  EXPECT_EQ(R"(Hello, <a href="kythe://foop">world</a>!)",
            RenderAsciiProtoDocument(R"(
      text {
        raw_text: "Hello, [world]!"
        link: { definition: "kythe://foo" }
      }
  )"));
}
TEST_F(HtmlRendererTest, DropMissingLink) {
  EXPECT_EQ(R"(Hello, world!)", RenderAsciiProtoDocument(R"(
      text {
        raw_text: "Hello, [world]!"
        link: { definition: "kythe://baz" }
      }
  )"));
}
TEST_F(HtmlRendererTest, EscapeLink) {
  EXPECT_EQ(R"(Hello, <a href="kythe://barp&amp;q=1&lt;">world</a>!)",
            RenderAsciiProtoDocument(R"(
      text {
        raw_text: "Hello, [world]!"
        link: { definition: "kythe://bar" }
      }
  )"));
}
TEST_F(HtmlRendererTest, RenderLinks) {
  EXPECT_EQ(
      R"(<a href="kythe://foop">Hello</a>, <a href="kythe://barp&amp;q=1&lt;">world</a>!)",
      RenderAsciiProtoDocument(R"(
      text {
        raw_text: "[Hello], [world][!]"
        link: { definition: "kythe://foo" }
        link: { definition: "kythe://bar" }
      }
  )"));
}
TEST_F(HtmlRendererTest, SkipMissingLinks) {
  EXPECT_EQ(
      R"(<a href="kythe://foop">Hello</a>, world<a href="kythe://barp&amp;q=1&lt;">!</a>)",
      RenderAsciiProtoDocument(R"(
      text {
        raw_text: "[Hello], [world][!]"
        link: { definition: "kythe://foo" }
        link: { }
        link: { definition: "kythe://bar" }
      }
  )"));
}
TEST_F(HtmlRendererTest, SkipNestedMissingLinks) {
  EXPECT_EQ(
      R"(<a href="kythe://foop">Hello</a>, world<a href="kythe://barp&amp;q=1&lt;">!</a>)",
      RenderAsciiProtoDocument(R"(
      text {
        raw_text: "[Hello], [world[!]]"
        link: { definition: "kythe://foo" }
        link: { }
        link: { definition: "kythe://bar" }
      }
  )"));
}
TEST_F(HtmlRendererTest, EscapeHtml) {
  EXPECT_EQ("&lt;&gt;&amp;&lt;&gt;&amp;[]\\", RenderAsciiProtoDocument(R"(
      text { raw_text: "<>&\\<\\>\\&\\[\\]\\\\" }
  )"));
}
TEST_F(HtmlRendererTest, JavadocTagBlocks) {
  EXPECT_EQ(
      "text\n<div class=\"kythe-doc-tag-section-title\">Author</div>"
      "<div class=\"kythe-doc-tag-section-content\"> a\n</div>"
      "<div class=\"kythe-doc-tag-section-title\">Author</div>"
      "<div class=\"kythe-doc-tag-section-content\"> b</div>",
      RenderJavadoc(R"(text
@author a
@author b)"));
}
TEST_F(HtmlRendererTest, JavadocTagBlockEmbedsCodeRef) {
  EXPECT_EQ(
      "text\n<div class=\"kythe-doc-tag-section-title\">Author</div>"
      "<div class=\"kythe-doc-tag-section-content\"> a <tt> robot</tt>\n</div>"
      "<div class=\"kythe-doc-tag-section-title\">Author</div>"
      "<div class=\"kythe-doc-tag-section-content\"> b</div>",
      RenderJavadoc(R"(text
@author a {@code robot}
@author b)"));
}
TEST_F(HtmlRendererTest, EmptyTags) {
  EXPECT_EQ("", RenderHtml("<I></I>"));
  EXPECT_EQ("<i></i>", RenderHtml("<I><B></B></I>"));
}
TEST_F(HtmlRendererTest, PassThroughStyles) {
  EXPECT_EQ(
      "<b><i><h1><h2><h3><h4><h5><h6>x</h6></h5></h4></h3></h2></h1></i></b>",
      RenderHtml("<B><I><H1><H2><H3><H4><H5><H6>x</H6></H5></H4></H3></H2></"
                 "H1></I></B>"));
}
TEST_F(HtmlRendererTest, PassThroughEntities) {
  EXPECT_EQ("&foo;", RenderHtml("&foo;"));
  EXPECT_EQ("&amp;&lt;bar&gt;;", RenderHtml("&<bar>;"));
}
TEST_F(HtmlRendererTest, RenderHtmlLinks) {
  EXPECT_EQ("<a href=\"foo.html\">bar</a>",
            RenderHtml("<A HREF = \"foo.html\" >bar</A>"));
  EXPECT_EQ("<a href=\"&quot;foo.html&quot;\">bar</a>",
            RenderHtml("<A HREF = \"&quot;foo.html&quot;\" >bar</A>"));
  EXPECT_EQ("&lt;A HREF = \"&amp; q;foo.html&amp; q;\" &gt;bar",
            RenderHtml("<A HREF = \"& q;foo.html& q;\" >bar</A>"));
  EXPECT_EQ("&lt;A HREF = \"href=\"foo.html\">\" BAD&gt;bar",
            RenderHtml("<A HREF = \"foo.html\" BAD>bar</A>"));
}
}  // anonymous namespace
}  // namespace kythe

int main(int argc, char **argv) {
  GOOGLE_PROTOBUF_VERIFY_VERSION;
  google::InitGoogleLogging(argv[0]);
  ::testing::InitGoogleTest(&argc, argv);
  int result = RUN_ALL_TESTS();
  return result;
}
