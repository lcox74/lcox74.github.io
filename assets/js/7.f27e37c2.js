(window.webpackJsonp=window.webpackJsonp||[]).push([[7],{338:function(t,e,n){},403:function(t,e,n){"use strict";n(338)},414:function(t,e,n){"use strict";n.r(e);var s=n(385),r={data:function(){return{repos:[]}},beforeMount:function(){var t=this;s.get("https://api.github.com/users/lcox74/repos?sort=updated").then((function(e){t.$data.repos=e.data})).catch((function(t){console.log("Here: "+t)}))}},o=(n(403),n(42)),i=Object(o.a)(r,(function(){var t=this,e=t.$createElement,n=t._self._c||e;return n("div",{staticClass:"git-repos-table"},[n("h1",[t._v("Projects")]),t._v(" "),n("table",[t._m(0),t._v(" "),n("tbody",t._l(t.repos,(function(e){return n("tr",{key:e.id},[n("td",[n("a",{attrs:{href:e.html_url}},[t._v(t._s(e.name)+" "+t._s(e.fork?"[Forked]":""))])]),t._v(" "),n("td",[t._v(t._s(e.description))]),t._v(" "),n("td",[t._v(t._s(e.language))])])})),0)])])}),[function(){var t=this.$createElement,e=this._self._c||t;return e("thead",[e("tr",[e("th",[this._v("Repo")]),this._v(" "),e("th",[this._v("Description")]),this._v(" "),e("th",[this._v("Language")])])])}],!1,null,"6c38e1d5",null);e.default=i.exports}}]);