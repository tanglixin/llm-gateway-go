package agent

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
)

// Eval 是一个安全的小型算术求值器（不执行任意代码）。
// 支持 + - * / 和括号，以及一元负号。
func Eval(expr string) (float64, error) {
	p := &parser{src: []rune(strings.ReplaceAll(expr, " ", ""))}
	v, err := p.parseExpr()
	if err != nil {
		return 0, err
	}
	if p.pos < len(p.src) {
		return 0, fmt.Errorf("表达式在 %q 处有多余字符", string(p.src[p.pos:]))
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("结果不是有限数")
	}
	return v, nil
}

type parser struct {
	src []rune
	pos int
}

func (p *parser) peek() rune {
	if p.pos < len(p.src) {
		return p.src[p.pos]
	}
	return 0
}

func (p *parser) parseExpr() (float64, error) {
	left, err := p.parseTerm()
	if err != nil {
		return 0, err
	}
	for {
		switch p.peek() {
		case '+':
			p.pos++
			right, err := p.parseTerm()
			if err != nil {
				return 0, err
			}
			left += right
		case '-':
			p.pos++
			right, err := p.parseTerm()
			if err != nil {
				return 0, err
			}
			left -= right
		default:
			return left, nil
		}
	}
}

func (p *parser) parseTerm() (float64, error) {
	left, err := p.parseFactor()
	if err != nil {
		return 0, err
	}
	for {
		switch p.peek() {
		case '*':
			p.pos++
			right, err := p.parseFactor()
			if err != nil {
				return 0, err
			}
			left *= right
		case '/':
			p.pos++
			right, err := p.parseFactor()
			if err != nil {
				return 0, err
			}
			if right == 0 {
				return 0, fmt.Errorf("除数为零")
			}
			left /= right
		default:
			return left, nil
		}
	}
}

func (p *parser) parseFactor() (float64, error) {
	switch p.peek() {
	case '-':
		p.pos++
		v, err := p.parseFactor()
		return -v, err
	case '+':
		p.pos++
		return p.parseFactor()
	case '(':
		p.pos++
		v, err := p.parseExpr()
		if err != nil {
			return 0, err
		}
		if p.peek() != ')' {
			return 0, fmt.Errorf("缺少右括号")
		}
		p.pos++
		return v, nil
	}

	start := p.pos
	for p.pos < len(p.src) && (unicode.IsDigit(p.src[p.pos]) || p.src[p.pos] == '.') {
		p.pos++
	}
	if start == p.pos {
		return 0, fmt.Errorf("位置 %d 处需要数字", p.pos)
	}
	return strconv.ParseFloat(string(p.src[start:p.pos]), 64)
}
