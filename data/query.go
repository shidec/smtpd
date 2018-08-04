package data

type StatQuery struct {
	Id 		string
	Count 	int
	Total	int
}

type ListQuery struct {
	Id 			string
	Sequence 	int
	Content		ContentQuery
}

type ContentQuery struct {
	Size		int
}


type UidlQuery struct {
	Id 			string
	Sequence 	int
	Uid			string
}