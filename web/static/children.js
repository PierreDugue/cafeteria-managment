// Search, selection and live counter for the children list.
// The page works without this script: it only adds convenience.
(function () {
  "use strict";

  // List selector: show the chosen list right away. The "Show" button stays
  // in the page for browsers without JavaScript; it is hidden here.
  var listSelect = document.getElementById("list-select");
  var listShow = document.getElementById("list-show");
  if (listSelect && listSelect.form) {
    listSelect.addEventListener("change", function () {
      listSelect.form.submit();
    });
    if (listShow) {
      listShow.hidden = true;
    }
  }

  var toolbar = document.getElementById("toolbar");
  var counter = document.getElementById("counter");
  var table = document.getElementById("children-table");
  var search = document.getElementById("search");
  var noResult = document.getElementById("no-result");
  if (!toolbar || !counter || !table || !search || !noResult) {
    return;
  }

  var rows = Array.prototype.slice.call(table.querySelectorAll("tbody tr[data-search]"));
  var template = counter.getAttribute("data-template") || "";

  // Matches textnorm.Key on the server for accents, case and spaces (enough
  // for French names), but not for every Unicode case.
  function normalize(s) {
    return s
      .normalize("NFD")
      .replace(/[̀-ͯ]/g, "")
      .toLowerCase()
      .split(/\s+/)
      .filter(Boolean)
      .join(" ");
  }

  function checkbox(row) {
    return row.querySelector('input[type="checkbox"]');
  }

  function updateCounter() {
    var children = 0;
    var emails = new Set();
    rows.forEach(function (row) {
      if (checkbox(row).checked) {
        children++;
        emails.add(row.getAttribute("data-email"));
      }
    });
    counter.textContent = template
      .replace("{children}", String(children))
      .replace("{emails}", String(emails.size));
  }

  function applyFilter() {
    var query = normalize(search.value);
    var visible = 0;
    rows.forEach(function (row) {
      var match = row.getAttribute("data-search").indexOf(query) !== -1;
      row.hidden = !match;
      if (match) {
        visible++;
      }
    });
    noResult.hidden = visible !== 0;
  }

  function setVisible(checked) {
    rows.forEach(function (row) {
      var box = checkbox(row);
      if (!row.hidden && !box.disabled) {
        box.checked = checked;
      }
    });
    updateCounter();
  }

  search.addEventListener("input", applyFilter);
  // Enter in the search box must not submit the form.
  search.addEventListener("keydown", function (e) {
    if (e.key === "Enter") {
      e.preventDefault();
    }
  });
  document.getElementById("select-all").addEventListener("click", function () {
    setVisible(true);
  });
  document.getElementById("select-none").addEventListener("click", function () {
    setVisible(false);
  });
  table.addEventListener("change", updateCounter);

  toolbar.hidden = false;
  counter.hidden = false;
  applyFilter();
  updateCounter();
})();
