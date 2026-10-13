package a

func keep() int {
	return 1
}

func moved(xs []int) int {
	n := 0
	for range xs {
		n++
	}
	return n
}
