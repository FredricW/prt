const LIMIT = 10;

class Widget {
  constructor(el) {
    this.el = el;
  }

  render() {
    this.el.textContent = "hello";
  }
}

function plain(a) {
  return a + 1;
}

module.exports = { Widget, plain, LIMIT };
